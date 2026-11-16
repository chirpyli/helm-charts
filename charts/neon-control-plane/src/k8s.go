package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
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
			Name   string            `json:"name"`
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
// compute 运行时状态查询（供 endpoint.Status 对账）
//
// 仅看 Deployment 对象是否存在不足以判断 compute 健康度：Pod 崩溃 / 镜像拉取失败 /
// 节点被驱逐时 Deployment 对象依然存在，但 compute 早已不可用。因此这里额外读取
// Deployment 的 readyReplicas，并在副本未就绪时进一步看 Pod 是否处于失败终端态。
// =============================================================================

// deploymentStatus 描述一个 compute Deployment 的运行时状态，供控制面对账 endpoint.Status。
type deploymentStatus struct {
	Exists        bool   // 是否存在对应 Deployment（404 视为不存在，属合法情况）
	ReadyReplicas int32  // status.readyReplicas
	Replicas      int32  // status.replicas
	PodFailed     bool   // 是否存在处于失败终端态的 Pod（CrashLoopBackOff / ImagePullBackOff / Failed 等）
	PodReason     string // Pod 失败原因（来自 containerStatuses.state.waiting.reason 或 pod phase）
}

// getDeploymentStatus 读取指定 compute Deployment 的运行时状态。
// Deployment 不存在（404）时返回 Exists=false 且不报错——这是 endpoint 处于 stopped 的合法情况。
func (k *kubeClient) getDeploymentStatus(name string) (deploymentStatus, error) {
	path := fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", k.namespace, name)
	data, code, err := k.doRequest(http.MethodGet, path, nil)
	if err != nil {
		return deploymentStatus{}, fmt.Errorf("get deployment %s: %w", name, err)
	}
	if code == 404 {
		return deploymentStatus{Exists: false}, nil
	}
	if code >= 300 {
		return deploymentStatus{}, fmt.Errorf("get deployment %s -> HTTP %d: %s", name, code, string(data))
	}
	var dep struct {
		Status struct {
			ReadyReplicas int32 `json:"readyReplicas"`
			Replicas      int32 `json:"replicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &dep); err != nil {
		return deploymentStatus{}, fmt.Errorf("parse deployment %s: %w", name, err)
	}
	ds := deploymentStatus{
		Exists:        true,
		ReadyReplicas: dep.Status.ReadyReplicas,
		Replicas:      dep.Status.Replicas,
	}
	// 副本未就绪时进一步看 Pod 是否处于失败终端态：避免把"崩溃循环"误报成"启动中"。
	if ds.ReadyReplicas == 0 {
		if podFailed, reason := k.getPodFailureReason(name); podFailed {
			ds.PodFailed = true
			ds.PodReason = reason
		}
	}
	return ds, nil
}

// podFailureWaitingReasons 容器内处于 waiting 状态时，哪些 reason 算"失败终端态"。
// 这些原因意味着 Pod 不会自愈，应当把 endpoint 标记为 failed 而不是 provisioning。
var podFailureWaitingReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"InvalidImageName":           true,
}

// getPodFailureReason 判断该 Deployment 下的 Pod 是否处于失败终端态。
// 通过 labelSelector=app.kubernetes.io/name=neon-compute,app.kubernetes.io/instance=<endpointID>
// 定位 Pod（endpointID 由 Deployment 名 neon-compute-<id> 反推）。
func (k *kubeClient) getPodFailureReason(deploymentName string) (bool, string) {
	endpointID := strings.TrimPrefix(deploymentName, "neon-compute-")
	sel := "app.kubernetes.io/name=neon-compute,app.kubernetes.io/instance=" + endpointID
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=%s",
		k.namespace, url.QueryEscape(sel))
	data, code, err := k.doRequest(http.MethodGet, path, nil)
	if err != nil || code >= 300 {
		return false, ""
	}
	var list struct {
		Items []struct {
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					State struct {
						Waiting struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
						Terminated struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return false, ""
	}
	for _, pod := range list.Items {
		if pod.Status.Phase == "Failed" {
			return true, "pod phase=Failed"
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Terminated.Reason != "" {
				return true, "container terminated: " + cs.State.Terminated.Reason
			}
			if podFailureWaitingReasons[cs.State.Waiting.Reason] {
				return true, cs.State.Waiting.Reason
			}
		}
	}
	return false, ""
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

// getServiceNodePort 回读 Service 实际分配的 NodePort 端口。
// NodePort Service 创建时 K8s 会在 30000-32767 范围内自动分配端口，
// 控制面必须回读后才能把正确的 <host>:<nodePort> 返回给用户。
// ClusterIP Service 没有 nodePort（返回 0）。
func (k *kubeClient) getServiceNodePort(name string) (int, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/services/%s", k.namespace, name)
	data, code, err := k.doRequest(http.MethodGet, path, nil)
	if err != nil {
		return 0, fmt.Errorf("get service %s: %w", name, err)
	}
	if code >= 300 {
		return 0, fmt.Errorf("get service %s -> HTTP %d: %s", name, code, string(data))
	}
	var svc struct {
		Spec struct {
			Ports []struct {
				NodePort int `json:"nodePort"`
			} `json:"ports"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &svc); err != nil {
		return 0, fmt.Errorf("parse service %s: %w", name, err)
	}
	if len(svc.Spec.Ports) == 0 {
		return 0, fmt.Errorf("service %s has no ports", name)
	}
	// 第一个端口（pg，5432）即用户连接入口。
	return svc.Spec.Ports[0].NodePort, nil
}

// =============================================================================
// 状态持久化已改为外部 PostgreSQL 独立库（见 store_postgres.go）。
// 此处不再保留任何状态快照读写实现；
// 下方 sendJSON 仍用于 compute Deployment/Service 与凭据 Secret 的 K8s API 调用。
// =============================================================================

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

// computeImagePullPolicy 返回 compute Pod 的镜像拉取策略；未配置或取值非法时回退 IfNotPresent。
//
// 为什么默认 IfNotPresent 而不是沿用 K8s 默认行为：
//  1. compute-node 镜像约 1.3GB，Always 会让每次 Pod 重建（endpoint 重建、节点漂移、
//     驱逐重启）都重新全量拉取一遍，endpoint 启动被拖到分钟级；
//  2. Always 把 Pod 可用性直接绑定在镜像仓库可用性上——仓库抖动、或镜像 manifest
//     被误删（本次 ImagePullBackOff 事故的直接原因）时，节点上明明已有镜像却仍起不来；
//  3. 控制面拉起的 compute 镜像由 settings.computeImage 固定指定，是"预先分发到各节点"的
//     确定性产物，不需要每次重新解析。
//
// 需要每次都拉最新（例如持续用 :latest 做开发迭代）时，显式配置为 Always 即可。
func computeImagePullPolicy() string {
	const defaultPolicy = "IfNotPresent"
	if cfg == nil || cfg.ComputeImagePullPolicy == "" {
		return defaultPolicy
	}
	switch cfg.ComputeImagePullPolicy {
	case "Always", "IfNotPresent", "Never":
		return cfg.ComputeImagePullPolicy
	default:
		// 非法值：回退默认而不是把整个 Deployment 创建打失败（K8s API 会拒绝非法枚举）。
		logWarn("computeImagePullPolicy: 非法取值 %q，回退 %s",
			cfg.ComputeImagePullPolicy, defaultPolicy)
		return defaultPolicy
	}
}

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
							"name":            "compute",
							"image":           ep.ComputeImage,
							"imagePullPolicy": computeImagePullPolicy(),
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

// buildComputeService 构造 compute 的**主 Service**（固定 ClusterIP）。
//
// 端口：5432（postgres）+ compute_ctl 端口（默认 3080，控制面用它推送 /configure）。
//
// 为什么主 Service 恒为 ClusterIP：K8s 的 NodePort Service 会给**所有**端口都分配 nodePort，
// 无法做到"只暴露 5432"。3080 是 compute_ctl 的管理端口（/configure、/status 等），
// 暴露到集群外等于把计算节点的控制权开放出去；因此主 Service 固定 ClusterIP，
// 需要集群外直连时另建 neon-compute-<endpoint>-ext（NodePort，仅 5432）。
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
				// compute_ctl 端口只暴露在集群内，供控制面推送重配置。
				map[string]interface{}{"name": "http", "port": computeCtlPort(), "targetPort": computeCtlPort()},
			},
		},
	}
}

// buildComputeExternalService 构造对外的 NodePort Service（**仅**暴露 5432）。
//
// 仅在 computeServiceType=NodePort 时创建，用于无 proxy / LoadBalancer 的裸机环境直连。
// 标签与主 Service 保持一致（app.kubernetes.io/name=neon-compute），
// 这样卸载时的 pre-delete 清理 Job 按标签批量删除时可以同时回收它。
func buildComputeExternalService(ep *Endpoint) map[string]interface{} {
	labels := map[string]interface{}{
		"app.kubernetes.io/name":     "neon-compute",
		"app.kubernetes.io/instance": ep.EndpointID,
	}
	return map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]interface{}{
			"name":      computeExternalServiceName(ep.EndpointID),
			"namespace": podNamespace(),
			"labels":    labels,
		},
		"spec": map[string]interface{}{
			"selector": labels,
			"type":     "NodePort",
			"ports": []interface{}{
				map[string]interface{}{"name": "pg", "port": 5432, "targetPort": 5432},
			},
		},
	}
}

// needsExternalService 是否需要额外的对外（NodePort）Service。
// 只有显式配置 computeServiceType=NodePort 时才创建，默认 ClusterIP 不创建。
func needsExternalService() bool {
	return cfg != nil && cfg.ComputeServiceType == "NodePort"
}

// computeDeploymentName 返回 endpoint 对应的 Deployment 名称。
func computeDeploymentName(endpointID string) string {
	return fmt.Sprintf("neon-compute-%s", endpointID)
}

// computeServiceName 返回 endpoint 对应的主 Service 名称（ClusterIP）。
func computeServiceName(endpointID string) string {
	return fmt.Sprintf("neon-compute-%s", endpointID)
}

// computeExternalServiceName 返回 endpoint 对应的对外 Service 名称（NodePort，仅 5432）。
func computeExternalServiceName(endpointID string) string {
	return fmt.Sprintf("neon-compute-%s-ext", endpointID)
}
