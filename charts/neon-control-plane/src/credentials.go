package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// =============================================================================
// 端点凭据治理
//
// 设计约束（docs/develop.md 5.4）：
//   - SCRAM 验证器写入独立 K8s Secret，业务库只保存引用（名称 + 键名）；
//   - get_endpoint_access_control 是连接鉴权热路径，必须走内存缓存，
//     避免每次鉴权都访问 kube-apiserver；
//   - 连接串、JWT、验证器、明文口令一律不打印到日志。
// =============================================================================

const (
	defaultCredentialSecretKey = "scram_verifier"
	credentialSecretPrefix     = "neon-cp-cred-"
)

type credentialCache struct {
	mu        sync.RWMutex
	verifiers map[string]string              // endpoint_id -> SCRAM 验证器
	refs      map[string]*EndpointCredential // endpoint_id -> Secret 引用
}

var credStore = &credentialCache{
	verifiers: map[string]string{},
	refs:      map[string]*EndpointCredential{},
}

func credentialSecretName(endpointID string) string {
	return credentialSecretPrefix + endpointID
}

// Warm 启动时把持久层加载的凭据引用同步到端点对象与缓存。
// 验证器内容延迟加载，避免启动阶段逐个访问 apiserver。
func (c *credentialCache) Warm(refs map[string]*EndpointCredential, eps map[string]*Endpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ref := range refs {
		c.refs[id] = ref
		if ep, ok := eps[id]; ok && ep != nil {
			ep.CredentialSecretName = ref.SecretName
			ep.CredentialSecretKey = ref.SecretKey
			ep.RoleName = ref.RoleName
			ep.DatabaseName = ref.DatabaseName
			ep.Owner = ref.Owner
		}
	}
}

// Get 返回端点的 SCRAM 验证器：内存缓存 → 进程内字段 → K8s Secret。
func (c *credentialCache) Get(ep *Endpoint) (string, error) {
	if ep == nil {
		return "", fmt.Errorf("端点不存在，无法解析凭据")
	}
	c.mu.RLock()
	v, ok := c.verifiers[ep.EndpointID]
	c.mu.RUnlock()
	if ok && v != "" {
		metricCredCacheHit.Add(1)
		return v, nil
	}
	metricCredCacheMiss.Add(1)

	// 本地 / 单测环境或刚创建的端点：进程内字段仍是有效来源。
	if ep.ScramVerifier != "" {
		c.mu.Lock()
		c.verifiers[ep.EndpointID] = ep.ScramVerifier
		c.mu.Unlock()
		return ep.ScramVerifier, nil
	}

	name := ep.CredentialSecretName
	if name == "" {
		name = credentialSecretName(ep.EndpointID)
	}
	key := ep.CredentialSecretKey
	if key == "" {
		key = defaultCredentialSecretKey
	}
	if kubePersist == nil {
		return "", fmt.Errorf("端点 %s 缺少 SCRAM 凭据（缓存未命中且不在 K8s 集群内）", ep.EndpointID)
	}
	verifier, err := readCredentialSecret(name, key)
	if err != nil {
		return "", fmt.Errorf("读取端点 %s 的凭据 Secret %s/%s 失败：%w", ep.EndpointID, name, key, err)
	}
	c.mu.Lock()
	c.verifiers[ep.EndpointID] = verifier
	c.mu.Unlock()
	return verifier, nil
}

// Ensure 创建/更新凭据 Secret，生成本地引用并预热缓存。
// 非集群环境下只写内存缓存，便于单测与本地调试。
func (c *credentialCache) Ensure(ep *Endpoint) (*EndpointCredential, error) {
	if ep == nil {
		return nil, fmt.Errorf("端点为空，无法创建凭据")
	}
	verifier := ep.ScramVerifier
	if verifier == "" && ep.Spec != nil && ep.Spec.Cluster != nil && len(ep.Spec.Cluster.Roles) > 0 {
		verifier = ep.Spec.Cluster.Roles[0].EncryptedPassword
	}
	if verifier == "" {
		return nil, fmt.Errorf("端点 %s 尚未生成 SCRAM 验证器", ep.EndpointID)
	}

	name := credentialSecretName(ep.EndpointID)
	key := defaultCredentialSecretKey
	if kubePersist != nil {
		if err := upsertCredentialSecret(name, key, verifier); err != nil {
			return nil, fmt.Errorf("写入凭据 Secret %s 失败：%w", name, err)
		}
	}

	ep.CredentialSecretName = name
	ep.CredentialSecretKey = key
	if ep.RoleName == "" && ep.Spec != nil && ep.Spec.Cluster != nil {
		if len(ep.Spec.Cluster.Roles) > 0 {
			ep.RoleName = ep.Spec.Cluster.Roles[0].Name
		}
		if len(ep.Spec.Cluster.Databases) > 0 {
			ep.DatabaseName = ep.Spec.Cluster.Databases[0].Name
			ep.Owner = ep.Spec.Cluster.Databases[0].Owner
		}
	}
	cred := &EndpointCredential{
		EndpointID:   ep.EndpointID,
		RoleName:     ep.RoleName,
		DatabaseName: ep.DatabaseName,
		Owner:        ep.Owner,
		SecretName:   name,
		SecretKey:    key,
	}
	c.mu.Lock()
	c.verifiers[ep.EndpointID] = verifier
	c.refs[ep.EndpointID] = cred
	c.mu.Unlock()
	return cred, nil
}

// AttachReference 为不新建角色的端点登记"共享凭据引用"。
//
// 背景（对齐 Neon 语义）：创建 compute endpoint 不会创建 role/database，
// 因此 read_only 副本与同分支主节点使用同一个角色的 SCRAM 验证器。
// 这里复用源端点的 Secret（名称 + 键名），既不生成新密码，也不写新的 Secret。
func (c *credentialCache) AttachReference(ep *Endpoint, roleName, databaseName, owner, secretName, secretKey string) *EndpointCredential {
	ep.RoleName = roleName
	ep.DatabaseName = databaseName
	ep.Owner = owner
	if ep.Owner == "" {
		ep.Owner = roleName
	}
	ep.CredentialSecretName = secretName
	ep.CredentialSecretKey = secretKey
	cred := &EndpointCredential{
		EndpointID:   ep.EndpointID,
		RoleName:     roleName,
		DatabaseName: databaseName,
		Owner:        ep.Owner,
		SecretName:   secretName,
		SecretKey:    secretKey,
	}
	c.mu.Lock()
	c.refs[ep.EndpointID] = cred
	c.mu.Unlock()
	return cred
}

// Invalidate 删除端点时清理缓存条目。
func (c *credentialCache) Invalidate(endpointID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.verifiers, endpointID)
	delete(c.refs, endpointID)
}

// Delete 删除凭据 Secret 与缓存，失败返回可行动错误。
func (c *credentialCache) Delete(endpointID string) error {
	c.Invalidate(endpointID)
	if kubePersist == nil {
		return nil
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s",
		kubePersist.namespace, credentialSecretName(endpointID))
	_, code, err := kubePersist.doRequest(http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("删除凭据 Secret：%w", err)
	}
	if code >= 300 && code != http.StatusNotFound {
		return fmt.Errorf("删除凭据 Secret 返回 HTTP %d", code)
	}
	return nil
}

// upsertCredentialSecret 创建或全量更新一个 Opaque Secret。
func upsertCredentialSecret(name, key, verifier string) error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", kubePersist.namespace, name)
	_, code, err := kubePersist.doRequest(http.MethodGet, path, nil)
	method := http.MethodPost
	apiPath := fmt.Sprintf("/api/v1/namespaces/%s/secrets", kubePersist.namespace)
	if err == nil && code == http.StatusOK {
		method = http.MethodPut
		apiPath = path
	}
	body := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": kubePersist.namespace,
		},
		"type": "Opaque",
		"data": map[string]interface{}{
			key: base64.StdEncoding.EncodeToString([]byte(verifier)),
		},
	}
	return kubePersist.sendJSON(method, apiPath, body)
}

// readCredentialSecret 读取 Opaque Secret 中指定键的值。
func readCredentialSecret(name, key string) (string, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", kubePersist.namespace, name)
	data, code, err := kubePersist.doRequest(http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", code)
	}
	var obj struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", fmt.Errorf("解析 Secret 响应：%w", err)
	}
	encoded, ok := obj.Data[key]
	if !ok {
		return "", fmt.Errorf("Secret 中缺少键 %s", key)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("解码 Secret 数据：%w", err)
	}
	return string(raw), nil
}
