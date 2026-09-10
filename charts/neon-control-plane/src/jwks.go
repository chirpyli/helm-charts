package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
)

// =============================================================================
// compute_ctl 鉴权用的 JWKS
//
// 背景：compute_ctl 外部 HTTP 服务（默认 3080）的 /configure、/status 等路由都在
// authenticated_router 上，用 Authorize::new(compute_id, instance_id, config.jwks) 校验
// （compute_tools/src/http/server.rs）。jwks 来自控制面 spec 响应里的
// compute_ctl_config.jwks —— 如果控制面下发空 JWKS，所有鉴权路由都会 401，
// 控制面就无法推送重配置（故障转移闭环会断在最后一公里）。
//
// 生成规则严格对齐 neon_local 的 create_jwks_from_pem
// （control_plane/src/endpoint.rs:158-187，其注释明确说明"与生产控制面的做法一致"）：
//   - kty = "OKP"，crv = "Ed25519"；
//   - x   = base64url(无填充) 的 32 字节裸公钥；
//   - kid = base64url(无填充) 的 sha256(裸公钥)；
//   - use = "sig"，key_ops = ["verify"]，alg = "EdDSA"。
//
// jsonwebtoken 侧按 AlgorithmParameters::OctetKeyPair 解析（jwk.rs:383-402），
// 并用 DecodingKey::from_ed_components(&params.x) 构造验签密钥（decoding.rs:185）。
// =============================================================================

// jwkSet 对应 jsonwebtoken::jwk::JwkSet：{"keys":[...]}。
type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// jwk 对应 jsonwebtoken::jwk::Jwk 的 OKP（Ed25519）形态。
type jwk struct {
	// Kty 密钥类型，Ed25519 固定为 "OKP"。
	Kty string `json:"kty"`
	// Crv 曲线名，固定为 "Ed25519"。
	Crv string `json:"crv"`
	// X base64url(无填充) 的 32 字节裸公钥。
	X string `json:"x"`
	// Use 用途：签名验证。
	Use string `json:"use,omitempty"`
	// Alg 算法：EdDSA。
	Alg string `json:"alg,omitempty"`
	// Kid base64url(无填充) 的 sha256(裸公钥)。
	Kid string `json:"kid,omitempty"`
	// KeyOps 允许的操作：仅验签。
	KeyOps []string `json:"key_ops,omitempty"`
}

// computeJWKS 全局 JWKS：控制面自己的 Ed25519 公钥，随 spec 下发给所有 compute。
// 为空表示未启用 JWT（此时 /configure 会 401，推送必然失败，日志会明确提示）。
var computeJWKS jwkSet

// initComputeJWKS 初始化 computeJWKS。
//
// 取值顺序：
//  1. JWT_PUBLIC_KEY_PATH 指向的 publicKey.pem（首选，与控制面校验入站请求用的是同一把公钥）；
//  2. 回退从 JWT_PRIVATE_KEY_PATH 的私钥派生（两者必然配对，不存在不一致的风险）。
//
// 失败不阻塞启动：控制面其它能力（project/branch/endpoint CRUD、spec 生成）不依赖 JWKS，
// 只是无法向 compute 推送重配置；这里打 WARN 让问题可见。
func initComputeJWKS() {
	raw, err := loadEd25519PublicKeyRaw()
	if err != nil {
		logWarn("jwks: 初始化失败，/configure 推送将不可用（compute 鉴权路由会 401）: %v", err)
		computeJWKS = jwkSet{Keys: []jwk{}}
		return
	}
	computeJWKS = buildJWKS(raw)
	log.Printf("jwks: compute_ctl JWKS ready (kid=%s)", computeJWKS.Keys[0].Kid)
}

// loadEd25519PublicKeyRaw 读取控制面 Ed25519 公钥的 32 字节裸密钥。
func loadEd25519PublicKeyRaw() ([]byte, error) {
	if p := os.Getenv("JWT_PUBLIC_KEY_PATH"); p != "" {
		raw, err := parseEd25519PublicPEM(p)
		if err == nil {
			return raw, nil
		}
		logWarn("jwks: 解析 JWT_PUBLIC_KEY_PATH=%s 失败，回退从私钥派生: %v", p, err)
	}
	if signer == nil {
		return nil, fmt.Errorf("jwt signer 未初始化，无法派生公钥")
	}
	if len(signer.pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("私钥派生出的公钥不是合法的 ed25519 公钥")
	}
	return signer.pub, nil
}

// parseEd25519PublicPEM 解析 PEM 格式的 SPKI 公钥，返回 32 字节裸公钥。
func parseEd25519PublicPEM(path string) ([]byte, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key %s: %w", path, err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("invalid PEM for public key %s", path)
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ed25519 public key: %w", err)
	}
	pub, ok := pubAny.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an ed25519 public key")
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("unexpected ed25519 public key size: %d", len(pub))
	}
	return pub, nil
}

// buildJWKS 由 32 字节裸公钥构造单密钥 JWKS。
func buildJWKS(raw []byte) jwkSet {
	sum := sha256.Sum256(raw)
	return jwkSet{
		Keys: []jwk{{
			Kty:    "OKP",
			Crv:    "Ed25519",
			X:      b64url(raw),
			Use:    "sig",
			Alg:    "EdDSA",
			Kid:    b64url(sum[:]),
			KeyOps: []string{"verify"},
		}},
	}
}

// jwksEnabled 返回是否已成功生成 JWKS（用于推送前快速判断与日志提示）。
func jwksEnabled() bool { return len(computeJWKS.Keys) > 0 }
