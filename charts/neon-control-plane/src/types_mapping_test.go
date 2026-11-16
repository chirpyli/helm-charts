package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// 凭据引用字段仅用于持久化与重建，不得泄漏到对外 API 的 JSON。
func TestEndpointCredentialFieldsNotSerialized(t *testing.T) {
	ep := &Endpoint{
		EndpointID:           "ep-mapping",
		CredentialSecretName: "neon-cp-cred-ep-mapping",
		CredentialSecretKey:  "scram_verifier",
		RoleName:             "cloud_admin",
		DatabaseName:         "neondb",
		Owner:                "cloud_admin",
		ScramVerifier:        "SCRAM-SHA-256$secret",
		ScramPassword:        "plain-password",
	}
	data, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, forbidden := range []string{
		"neon-cp-cred-ep-mapping", "scram_verifier", "cloud_admin", "neondb",
		"SCRAM-SHA-256$secret", "plain-password",
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("对外 JSON 不应包含 %q：%s", forbidden, s)
		}
	}
}

func TestStateStoreConfigJSONTags(t *testing.T) {
	c := StateStoreConfig{Backend: "postgres", ConnectTimeoutSeconds: 5, MaxConnections: 8}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"backend"`, `"connect_timeout_seconds"`, `"max_connections"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("配置缺少键 %s：%s", key, string(data))
		}
	}
}
