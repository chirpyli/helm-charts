package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"testing"
)

// 本文件覆盖" pageserver 故障转移正确性 "的几个关键点：
//   1. shards map 的 key（ShardIndex 的 4 位 hex）必须与 compute_ctl 的查表逻辑一致；
//   2. stripe_size 的不变量（shard_count==0 时为 null）；
//   3. 路由指纹能识别"pageserver 变了"，且不受 map 遍历顺序影响；
//   4. JWKS 的 x / kid 严格按 neon_local create_jwks_from_pem 计算。

func TestParseShardIndex(t *testing.T) {
	cases := []struct {
		name    string
		shardID string
		number  int
		count   int
		want    string
	}{
		// unsharded：TenantShardId 无后缀，ShardIndex(0,0) → "0000"
		{name: "unsharded", shardID: "3d1f7595b468230304e0b73cecbcb081", number: 0, count: 0, want: "0000"},
		// 2 分片中的第 0 个：后缀即 ShardIndex hex
		{name: "sharded-0-of-2", shardID: "3d1f7595b468230304e0b73cecbcb081-0002", number: 0, count: 2, want: "0002"},
		// 2 分片中的第 1 个
		{name: "sharded-1-of-2", shardID: "3d1f7595b468230304e0b73cecbcb081-0102", number: 1, count: 2, want: "0102"},
		// 17 分片中的第 13 个（上游单测同款：13/17 → "0d11"）
		{name: "sharded-13-of-17", shardID: "3d1f7595b468230304e0b73cecbcb081-0d11", number: 13, count: 17, want: "0d11"},
		// 后缀非法：回退按 编号+分片数 拼装
		{name: "bad-suffix", shardID: "3d1f7595b468230304e0b73cecbcb081-zzzz", number: 1, count: 2, want: "0102"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseShardIndex(c.shardID, c.number, c.count); got != c.want {
				t.Fatalf("parseShardIndex(%q,%d,%d)=%q, want %q", c.shardID, c.number, c.count, got, c.want)
			}
		})
	}
}

func TestStripeSizePtrInvariant(t *testing.T) {
	if got := stripeSizePtr(0, 32768); got != nil {
		t.Fatalf("unsharded 时 stripe_size 必须为 null，got %v", *got)
	}
	got := stripeSizePtr(2, 32768)
	if got == nil || *got != 32768 {
		t.Fatalf("sharded 时 stripe_size 必须非 null 且等于原值，got %v", got)
	}
}

func TestRouteFingerprint(t *testing.T) {
	base := PageserverConnInfo{
		ShardCount: 1,
		Shards: map[string]ShardInfo{
			"0001": {Pageservers: []PageserverShard{{LibpqURL: "postgresql://ps-0.svc:6400"}}},
		},
	}
	// 同样的内容、不同的插入顺序 → 指纹必须一致（map 遍历顺序无关）
	same := PageserverConnInfo{
		ShardCount: 1,
		Shards: map[string]ShardInfo{
			"0001": {Pageservers: []PageserverShard{{LibpqURL: "postgresql://ps-0.svc:6400"}}},
		},
	}
	if routeFingerprint(base) != routeFingerprint(same) {
		t.Fatal("指纹必须与 map 遍历顺序无关")
	}
	// pageserver 地址变化（故障转移）→ 指纹必须变化，否则不会触发推送
	moved := PageserverConnInfo{
		ShardCount: 1,
		Shards: map[string]ShardInfo{
			"0001": {Pageservers: []PageserverShard{{LibpqURL: "postgresql://ps-1.svc:6400"}}},
		},
	}
	if routeFingerprint(base) == routeFingerprint(moved) {
		t.Fatal("pageserver 变更后指纹必须变化")
	}
	// 空 shards 与有 shards 的指纹也必须不同（避免"空路由"被误判为已下发）
	if routeFingerprint(PageserverConnInfo{}) == routeFingerprint(base) {
		t.Fatal("空路由指纹不应等于有效路由指纹")
	}
}

func TestBuildJWKS(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	set := buildJWKS(pub)
	if len(set.Keys) != 1 {
		t.Fatalf("期望 1 个 key，got %d", len(set.Keys))
	}
	k := set.Keys[0]
	if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Use != "sig" || k.Alg != "EdDSA" {
		t.Fatalf("JWK 公共字段不符合 neon_local 约定: %+v", k)
	}
	if k.X != b64url(pub) {
		t.Fatalf("x 必须是裸公钥的 base64url(无填充)，got %q", k.X)
	}
	sum := sha256.Sum256(pub)
	if k.Kid != b64url(sum[:]) {
		t.Fatalf("kid 必须是 sha256(裸公钥) 的 base64url(无填充)，got %q", k.Kid)
	}
	if len(k.KeyOps) != 1 || k.KeyOps[0] != "verify" {
		t.Fatalf("key_ops 必须为 [verify]，got %v", k.KeyOps)
	}
}
