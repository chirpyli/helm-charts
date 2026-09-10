package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// ComputeSpec 对应 libs/compute_api/src/spec.rs 的核心字段（phase-1 最小集）。
type ComputeSpec struct {
	FormatVersion            float64            `json:"format_version"`
	TenantID                 string             `json:"tenant_id"`
	TimelineID               string             `json:"timeline_id"`
	Mode                     string             `json:"mode"`
	PageserverConnectionInfo PageserverConnInfo `json:"pageserver_connection_info"`
	SafekeeperConnstrings    []string           `json:"safekeeper_connstrings"`
	// SafekeepersGeneration safekeeper 成员配置 generation。
	// 对应上游 ComputeSpec::safekeepers_generation（Option<u32>），compute_ctl 会写入
	// neon.safekeepers GUC 的 g#<generation>: 前缀：
	//   1) 非 0 值强制 walproposer 使用成员配置；
	//   2) walproposer 用它比较成员配置的新旧，避免应用过期集合。
	// 指针 + omitempty：未知时不下发（上游 serde(default) 视为 None）。
	SafekeepersGeneration *int         `json:"safekeepers_generation,omitempty"`
	StorageAuthToken      string       `json:"storage_auth_token"`
	ProjectID             string       `json:"project_id,omitempty"`
	BranchID              string       `json:"branch_id,omitempty"`
	EndpointID            string       `json:"endpoint_id,omitempty"`
	Cluster               *ClusterSpec `json:"cluster,omitempty"`
	// LocalProxyConfig 为本地 proxy 配置（含 JWKS），新版本 compute_ctl 要求此字段存在。
	// Phase-1 不使用 JWT 认证，下发空的 jwks 列表即可。
	LocalProxyConfig     *LocalProxySpec `json:"local_proxy_config"`
	EndpointStorageAddr  string          `json:"endpoint_storage_addr,omitempty"`
	EndpointStorageToken string          `json:"endpoint_storage_token,omitempty"`
	// suspend_timeout_seconds 为必填字段（新版本 compute_ctl 引入），0 表示不自动暂停。
	SuspendTimeoutSeconds int64 `json:"suspend_timeout_seconds"`
	// DatabricksSettings 为可选字段，compute_ctl 新版本支持，先省略
}

// =============================================================================
// 本地 Proxy 配置（compute_ctl 内置 local_proxy 用）
// =============================================================================

// LocalProxySpec 本地 proxy 的 JWT 认证与 TLS 配置。
// 对应 libs/compute_api/src/spec.rs 中的 LocalProxySpec。
type LocalProxySpec struct {
	Jwks []JwksSettings `json:"jwks"`
	Tls  *TlsConfig     `json:"tls,omitempty"`
}

// JwksSettings JWKS 端点配置（用于 proxy 验证客户端 JWT）。
// 对应 libs/compute_api/src/spec.rs 中的 JwksSettings。
type JwksSettings struct {
	ID           string   `json:"id"`
	RoleNames    []string `json:"role_names"`
	JwksURL      string   `json:"jwks_url"`
	ProviderName string   `json:"provider_name"`
	JwtAudience  string   `json:"jwt_audience,omitempty"`
}

// TlsConfig 本地 proxy 的 TLS 证书路径。
type TlsConfig struct {
	KeyPath  string `json:"key_path"`
	CertPath string `json:"cert_path"`
}

// PageserverConnInfo 对应 libs/compute_api/src/spec.rs 的 PageserverConnectionInfo。
// 注意 shard 的 pageserver 用 libpq_url + grpc_url，没有 host/port/http_host/http_port。
//
// 不变量（上游注释原文）：
//   - shard_count：0 表示 unsharded，1 表示 1 个分片的 sharded 租户，以此类推；
//   - stripe_size：shard_count == 0 时必须为 null，否则必须非 null。
type PageserverConnInfo struct {
	ShardCount int                  `json:"shard_count"`
	StripeSize *int                 `json:"stripe_size"`
	Shards     map[string]ShardInfo `json:"shards"`
}

type ShardInfo struct {
	Pageservers []PageserverShard `json:"pageservers"`
}

type PageserverShard struct {
	ID       *int   `json:"id,omitempty"`
	LibpqURL string `json:"libpq_url"`
	// GRPCURL 上游为 Option<String>：未启用 gRPC 的 pageserver 不下发该字段（omitempty）。
	GRPCURL *string `json:"grpc_url,omitempty"`
}

// stripeSizePtr 生成 PageserverConnInfo.StripeSize（*int）。
// 严格遵循上游不变量：shard_count == 0（unsharded）时为 null，否则必须非 null。
func stripeSizePtr(shardCount, stripeSize int) *int {
	if shardCount <= 0 {
		return nil
	}
	v := stripeSize
	return &v
}

type ClusterSpec struct {
	Roles     []RoleSpec     `json:"roles"`
	Databases []DatabaseSpec `json:"databases"`
	// settings 在 Rust 端是 GenericOptions = Option<Vec<GenericOption>>，不是 map。
	// 传 null 表示无额外设置；否则传 []GenericOption 数组。
	Settings       *[]GenericOption `json:"settings,omitempty"`
	PostgresqlConf string           `json:"postgresql_conf,omitempty"`
}

// GenericOption 对应 Rust GenericOption { name, value, vartype }。
// control plane 对通用设置不敏感，此处仅用于保持 JSON 结构兼容。
type GenericOption struct {
	Name    string  `json:"name"`
	Value   *string `json:"value,omitempty"`
	Vartype string  `json:"vartype"`
}

type RoleSpec struct {
	Name              string `json:"name"`
	EncryptedPassword string `json:"encrypted_password"`
}

type DatabaseSpec struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// generateScramVerifier 生成 SCRAM-SHA-256 验证器字符串。
// compute_ctl 把该串写入 PG 的 pg_auth；proxy 用 scram::ServerSecret::parse 解析同一串做鉴权，
// 二者必须逐字节一致。
//
// 重要：字段顺序与分隔符必须严格遵循 Postgres / Neon 上游约定（见
// neon/libs/proxy/postgres-protocol2/src/password/mod.rs）：
//
//	SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>
//
// 即“迭代次数在前、salt 在后，二者用冒号 ':' 分隔”。若写成
// "SCRAM-SHA-256$<salt>$<iterations>$..."（salt 在前），Postgres 无法按
// <iterations>:<salt> 解析，会误把整串当成明文密码重新哈希，导致最终写入
// pg_authid 的 verifier 与客户端口令不匹配，表现为 “password authentication failed”。
func generateScramVerifier(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const iters = 4096
	salted := pbkdf2SHA256([]byte(password), salt, iters, 32)
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, []byte("Server Key"))
	// 严格对齐 Postgres/Neon：iterations:salt$StoredKey:ServerKey
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s",
		iters,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(storedKey[:]),
		base64.StdEncoding.EncodeToString(serverKey),
	), nil
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// pbkdf2SHA256 手动实现 PBKDF2-HMAC-SHA256（避免引入外部依赖）。
func pbkdf2SHA256(password, salt []byte, iters, keyLen int) []byte {
	const hLen = sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, numBlocks*hLen)
	for block := 1; block <= numBlocks; block++ {
		// U1 = HMAC(password, salt || INT_32_BE(block))
		buf := make([]byte, len(salt)+4)
		copy(buf, salt)
		buf[len(salt)] = byte(block >> 24)
		buf[len(salt)+1] = byte(block >> 16)
		buf[len(salt)+2] = byte(block >> 8)
		buf[len(salt)+3] = byte(block)
		u := hmacSHA256(password, buf)
		t := append([]byte{}, u...)
		for i := 2; i <= iters; i++ {
			u = hmacSHA256(password, u)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
