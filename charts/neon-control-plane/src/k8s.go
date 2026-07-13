package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// kubeClient 通过 in-cluster ServiceAccount 直接调用 Kubernetes API（无 client-go 依赖）。
type kubeClient struct {
	base      string
	token     string
	namespace string
	http      *http.Client
}

func newKubeClient() (*kubeClient, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not running in cluster (KUBERNETES_SERVICE_HOST/PORT unset)")
	}
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, err
	}
	caCert, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caCert)
	ns := "default"
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		ns = string(bytes.TrimSpace(b))
	}
	return &kubeClient{
		base:      fmt.Sprintf("https://%s:%s", host, port),
		token:     string(bytes.TrimSpace(token)),
		namespace: ns,
		http:      &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
	}, nil
}

// doRequest 发送 K8s API 请求。
func (k *kubeClient) doRequest(method, path string, body map[string]interface{}) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, k.base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

// =============================================================================
// Deployment 操作
// =============================================================================

// createDeployment 创建一个 Deployment（等价于 kubectl create -f）。
func (k *kubeClient) createDeployment(dep map[string]interface{}) error {
	b, err := json.Marshal(dep)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/apis/apps/v1/namespaces/%s/deployments", k.base, k.namespace), bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create deployment -> HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// deleteDeployment 删除指定名称的 Deployment。
func (k *kubeClient) deleteDeployment(name string) error {
	path := fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", k.namespace, name)
	data, code, err := k.doRequest(http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("delete deployment %s: %w", name, err)
	}
	if code >= 300 && code != 404 {
		return fmt.Errorf("delete deployment %s -> HTTP %d: %s", name, code, string(data))
	}
	return nil
}

// listDeploymentsResp K8s Deployment List API 的简化响应结构。
type listDeploymentsResp struct {
	Items []struct {
		Metadata struct {
			Name   string `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	} `json:"items"`
}

// listComputeDeployments 列出命名空间内所有 neon-compute 类 Deployment。
// 用于启动对账：以 K8s 实际状态为准修正 endpoint 状态。
func (k *kubeClient) listComputeDeployments() ([]string, error) {
	path := fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments?labelSelector=app.kubernetes.io/name%%3Dneon-compute", k.namespace)
	data, code, err := k.doRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	if code >= 300 {
		return nil, fmt.Errorf("list deployments -> HTTP %d: %s", code, string(data))
	}
	var result listDeploymentsResp
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse deployment list: %w", err)
	}
	var names []string
	for _, item := range result.Items {
		names = append(names, item.Metadata.Name)
	}
	return names, nil
}

// =============================================================================
// Service 操作
// =============================================================================

// createService 为 compute 创建一个 ClusterIP Service，暴露 PostgreSQL 端口。
func (k *kubeClient) createService(svc map[string]interface{}) error {
	b, err := json.Marshal(svc)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/services", k.namespace)
	req, _ := http.NewRequest(http.MethodPost, k.base+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 409 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create service -> HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// deleteService 删除指定名称的 Service。
func (k *kubeClient) deleteService(name string) error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/services/%s", k.namespace, name)
	data, code, err := k.doRequest(http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("delete service %s: %w", name, err)
	}
	if code >= 300 && code != 404 {
		return fmt.Errorf("delete service %s -> HTTP %d: %s", name, code, string(data))
	}
	return nil
}

// =============================================================================
// ConfigMap 操作
// =============================================================================

const stateConfigMapName = "neon-cp-state"

// readConfigMap 读取指定 ConfigMap 的内容。
func (k *kubeClient) readConfigMap(name string) (map[string]string, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", k.namespace, name)
	data, code, err := k.doRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("read configmap %s: %w", name, err)
	}
	if code == 404 {
		return nil, nil // ConfigMap 不存在，返回 nil 表示首次启动
	}
	if code >= 300 {
		return nil, fmt.Errorf("read configmap %s -> HTTP %d: %s", name, code, string(data))
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(data, &cm); err != nil {
		return nil, fmt.Errorf("parse configmap: %w", err)
	}
	return cm.Data, nil
}

// upsertConfigMap 创建或更新 ConfigMap（PUT 全量替换，相当于 apply）。
func (k *kubeClient) upsertConfigMap(name string, cmData map[string]interface{}) error {
	// 先 GET 判断是否存在
	path := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", k.namespace, name)
	_, code, _ := k.doRequest(http.MethodGet, path, nil)

	method := http.MethodPost
	apiPath := fmt.Sprintf("/api/v1/namespaces/%s/configmaps", k.namespace)
	if code == 200 {
		// 已存在 → PUT 更新
		method = http.MethodPut
		apiPath = path
	}

	body := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": k.namespace,
			"labels": map[string]string{
				"app.kubernetes.io/name":    "neon-control-plane",
				"app.kubernetes.io/part-of": "neon",
			},
		},
		"data": cmData,
	}
	return k.sendJSON(method, apiPath, body)
}

// sendJSON 发送 JSON body 到 K8s API。
func (k *kubeClient) sendJSON(method, path string, body interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, k.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("K8s API %s %s -> HTTP %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	return nil
}

// =============================================================================
// compute Deployment / Service 构造
// =============================================================================

// buildComputeDeployment 构造 compute Pod 的 Deployment 清单。
// compute_ctl 从控制面拉取 spec（GET /compute/api/v2/computes/{id}/spec）。
//
// 镜像要求：必须使用 neondatabase/compute-node-v{PGVersion} 镜像
// 因为 compute_ctl 二进制只存在于 compute-node 专用镜像中，路径为 /usr/local/bin/compute_ctl。
// 对应的 PostgreSQL 二进制路径为 /usr/local/bin/postgres。
func buildComputeDeployment(ep *Endpoint) map[string]interface{} {
	labels := map[string]interface{}{
		"app.kubernetes.io/name":     "neon-compute",
		"app.kubernetes.io/instance": ep.EndpointID,
	}
	controlPlaneURI := fmt.Sprintf("http://neon-control-plane-svc:%d", cfg.ListenPort)
	return map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":      computeDeploymentName(ep.EndpointID),
			"namespace": podNamespace(),
			"labels":    labels,
		},
		"spec": map[string]interface{}{
			"replicas": 1,
			"selector": map[string]interface{}{"matchLabels": labels},
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{"labels": labels},
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "compute",
							"image": ep.ComputeImage,
							"command": []interface{}{
								// compute_ctl 在 compute-node 镜像中位于 /usr/local/bin/；
								// 使用 bash -c 确保 PATH 包含该路径（与 neon-operator 一致）。
								"bash", "-c",
								fmt.Sprintf(
									"exec /usr/local/bin/compute_ctl -C postgresql://cloud_admin@localhost/postgres "+
										"--control-plane-uri %s "+
										"--compute-id %s "+
										"--pgdata /var/db/postgres/data "+
										"--pgbin /usr/local/bin/postgres "+
										"--external-http-port 3080",
									controlPlaneURI, ep.EndpointID,
								),
							},
							"ports": []interface{}{
								map[string]interface{}{"containerPort": 5432, "name": "pg"},
								map[string]interface{}{"containerPort": 3080, "name": "http"},
							},
						},
					},
				},
			},
		},
	}
}

// buildComputeService 构造 compute Pod 的 ClusterIP Service。
func buildComputeService(ep *Endpoint) map[string]interface{} {
	labels := map[string]interface{}{
		"app.kubernetes.io/name":     "neon-compute",
		"app.kubernetes.io/instance": ep.EndpointID,
	}
	return map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]interface{}{
			"name":      computeServiceName(ep.EndpointID),
			"namespace": podNamespace(),
			"labels":    labels,
		},
		"spec": map[string]interface{}{
			"selector": labels,
			"type":     "ClusterIP",
			"ports": []interface{}{
				map[string]interface{}{"name": "pg", "port": 5432, "targetPort": 5432},
				map[string]interface{}{"name": "http", "port": 3080, "targetPort": 3080},
			},
		},
	}
}

// computeDeploymentName 返回 endpoint 对应的 Deployment 名称。
func computeDeploymentName(endpointID string) string {
	return fmt.Sprintf("neon-compute-%s", endpointID)
}

// computeServiceName 返回 endpoint 对应的 Service 名称。
func computeServiceName(endpointID string) string {
	return fmt.Sprintf("neon-compute-%s", endpointID)
}
