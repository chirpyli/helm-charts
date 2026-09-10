package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"
)

// jwtSigner 用 Ed25519 私钥签发 neon 兼容的 JWT，同时持有公钥用于校验。
// neon 的 JWT：alg=EdDSA，载荷仅需 scope（字符串枚举，如 "admin"/"pageserverapi"/"tenant"）。
type jwtSigner struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

func newJWTSigner(keyPath string) (*jwtSigner, error) {
	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read private key %s: %w", keyPath, err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("invalid PEM for private key %s", keyPath)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ed25519 private key: %w", err)
	}
	pk, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an ed25519 private key")
	}
	return &jwtSigner{priv: pk, pub: pk.Public().(ed25519.PublicKey)}, nil
}

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// sign 签发 scope 令牌（blanket 访问，tenant_id 为空）。
func (s *jwtSigner) sign(scope string) (string, error) {
	return s.signWithTenant(scope, "")
}

// signWithTenant 签发带 tenant_id 的 scope 令牌（如 compute 使用的 "tenant" scope）。
func (s *jwtSigner) signWithTenant(scope, tenantID string) (string, error) {
	header := map[string]string{"alg": "EdDSA", "typ": "JWT"}
	payload := map[string]interface{}{"scope": scope}
	if tenantID != "" {
		payload["tenant_id"] = tenantID
	}
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	hb := b64url(h)
	pb := b64url(p)
	toSign := []byte(hb + "." + pb)
	sig := ed25519.Sign(s.priv, toSign)
	return hb + "." + pb + "." + b64url(sig), nil
}

// verify 校验 JWT 令牌签名和 scope。
// 返回 payload 中的 scope 字段；若签名无效或格式错误则返回空字符串。
// 用于 notify 回调的 ControlPlane scope 鉴权。
func (s *jwtSigner) verify(token string) (scope string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid jwt: expected 3 parts, got %d", len(parts))
	}

	// 解码 header（校验 alg）
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("decode header: %w", err)
	}
	var header map[string]string
	if err := json.Unmarshal(hb, &header); err != nil {
		return "", fmt.Errorf("decode header json: %w", err)
	}
	if alg, ok := header["alg"]; !ok || alg != "EdDSA" {
		return "", fmt.Errorf("unsupported alg, expected EdDSA")
	}

	// 解码 payload
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}

	// 解码签名
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("decode signature: %w", err)
	}

	// 校验 Ed25519 签名
	toVerify := []byte(parts[0] + "." + parts[1])
	if !ed25519.Verify(s.pub, toVerify, sig) {
		return "", fmt.Errorf("invalid signature")
	}

	// 解析 payload 中的 scope
	var payload map[string]interface{}
	if err := json.Unmarshal(pb, &payload); err != nil {
		return "", fmt.Errorf("decode payload json: %w", err)
	}
	scopeVal, ok := payload["scope"].(string)
	if !ok {
		return "", fmt.Errorf("scope not found in jwt payload")
	}
	return scopeVal, nil
}

// publicKeyHash 返回公钥的 SHA256 哈希（用于调试）。
func (s *jwtSigner) publicKeyHash() string {
	sum := sha256.Sum256(s.pub)
	return fmt.Sprintf("%x", sum)
}

// computeTokenTTL 控制面调用 compute_ctl 时所用 JWT 的有效期。
//
// compute_ctl 的 Authorize 把 required_spec_claims 清空了
// （compute_tools/src/http/middleware/authorize.rs:49），因此 exp 不是必需项；
// 但 validate_exp 仍为 true（存在 exp 时会校验是否过期），所以带上一个合理的有效期更稳妥，
// 也能在未来上游收紧 exp 校验时保持一致。
const computeTokenTTL = 1 * time.Hour

// signComputeToken 签发调用 compute_ctl 用的 JWT（compute-scoped）。
//
// 对齐 neon_local 的 Endpoint::generate_jwt(None::<ComputeClaimsScope>)
// （control_plane/src/endpoint.rs:691-703）：非 Admin scope 时
//
//	ComputeClaims { compute_id: Some(endpoint_id), scope: None, audience: None }
//
// compute_ctl 的校验分支（compute_tools/src/http/middleware/authorize.rs:93-133）：
//   - scope == Admin：要求 aud 包含 "compute"；
//   - 其它（含缺省）：要求 claims.compute_id 等于该 compute 自身的 compute_id。
//
// 因此这里只声明 compute_id，不写 scope。
func (s *jwtSigner) signComputeToken(computeID string) (string, error) {
	header := map[string]string{"alg": "EdDSA", "typ": "JWT"}
	payload := map[string]interface{}{
		"compute_id": computeID,
		"exp":        time.Now().Add(computeTokenTTL).Unix(),
	}
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	hb := b64url(h)
	pb := b64url(p)
	toSign := []byte(hb + "." + pb)
	sig := ed25519.Sign(s.priv, toSign)
	return hb + "." + pb + "." + b64url(sig), nil
}
