package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"
)

// nowRFC3339 返回 API 视图使用的时间戳格式。
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// =============================================================================
// 分支级 catalog（roles / databases）的通用辅助
//
// 设计（docs/dev/control-plane-api-step1-step2.md）：
//   - catalog 属于 branch，endpoint 只是访问入口；
//   - 写操作统一通过 ComputeSpec + compute_ctl /configure 生效；
//   - 同一分支的目录写串行化，避免并发请求基于旧 spec 互相覆盖。
// =============================================================================

// pgIdentRe 限制角色名/数据库名为无需引号的 PG 标识符。
var pgIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)

// validPgIdent 校验角色名 / 数据库名。
func validPgIdent(name string) bool {
	return pgIdentRe.MatchString(name)
}

// branchLocks 每个 branch 一把互斥锁；只在进程内生效（控制面单副本）。
var branchLocks sync.Map

// lockBranch 获取分支锁，返回解锁函数。
func lockBranch(branchID string) func() {
	v, _ := branchLocks.LoadOrStore(branchID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// apiError 携带 HTTP 状态码的错误，便于 handler 统一映射。
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func newAPIError(status int, format string, args ...interface{}) *apiError {
	return &apiError{status: status, msg: fmt.Sprintf(format, args...)}
}

// writeAPIError 把 apiError 映射为 JSON 响应；非 apiError 统一 500。
func writeAPIError(w http.ResponseWriter, err error) {
	if ae, ok := err.(*apiError); ok {
		writeJSON(w, ae.status, map[string]string{"error": ae.msg})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

// catalogForBranch 返回分支 catalog 的值拷贝（按名称升序），避免调用方持有指针。
func catalogForBranch(branchID string) ([]Role, []Database) {
	st.mu.RLock()
	defer st.mu.RUnlock()

	roles := make([]Role, 0)
	for _, r := range st.roles {
		if r.BranchID == branchID {
			roles = append(roles, *r)
		}
	}
	dbs := make([]Database, 0)
	for _, d := range st.databases {
		if d.BranchID == branchID {
			dbs = append(dbs, *d)
		}
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].Name < dbs[j].Name })
	return roles, dbs
}

// findWritableEndpointLocked 返回分支上可承载目录变更的 read_write endpoint。
// 优先 running；没有 running 时返回 provisioning 的主节点（调用方决定是否拒绝）。
// 调用方必须持有 st.mu 读锁或写锁。
func findWritableEndpointLocked(branchID string) *Endpoint {
	var running, fallback *Endpoint
	for _, ep := range st.endpoints {
		if ep == nil || ep.BranchID != branchID {
			continue
		}
		if ep.Type != "" && ep.Type != EndpointTypeReadWrite {
			continue
		}
		if ep.Status == EndpointStatusRunning {
			if running == nil || ep.EndpointID < running.EndpointID {
				running = ep
			}
			continue
		}
		if fallback == nil || ep.EndpointID < fallback.EndpointID {
			fallback = ep
		}
	}
	if running != nil {
		return running
	}
	return fallback
}

// requireBranch 校验 project/branch 归属；失败返回 404。
func requireBranch(projectID, branchID string) (*Branch, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if _, ok := st.projects[projectID]; !ok {
		return nil, newAPIError(http.StatusNotFound, "branch not found")
	}
	b, ok := st.branches[branchID]
	if !ok || b.ProjectID != projectID {
		return nil, newAPIError(http.StatusNotFound, "branch not found")
	}
	return b, nil
}

// requireWritableEndpoint 要求分支存在 running 的 read_write endpoint。
func requireWritableEndpoint(branchID string) (*Endpoint, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	ep := findWritableEndpointLocked(branchID)
	if ep == nil {
		return nil, newAPIError(http.StatusConflict, "branch has no read_write endpoint")
	}
	if ep.Status != EndpointStatusRunning {
		return nil, newAPIError(http.StatusConflict,
			"branch read_write endpoint is not running (status=%s)", ep.Status)
	}
	return ep, nil
}

// buildSpecCatalog 从内存 registry 构建 roles/databases，并逐个解析 verifier。
// 不持有 st.mu 做网络 IO：先快照，再读 Secret。
func buildSpecCatalog(branchID string) ([]RoleSpec, []DatabaseSpec, error) {
	roles, dbs := catalogForBranch(branchID)

	roleSpecs := make([]RoleSpec, 0, len(roles))
	for _, r := range roles {
		verifier, err := credStore.GetSecret(r.SecretName, r.SecretKey)
		if err != nil {
			return nil, nil, fmt.Errorf("读取角色 %s 的 SCRAM 凭据：%w", r.Name, err)
		}
		roleSpecs = append(roleSpecs, RoleSpec{Name: r.Name, EncryptedPassword: verifier})
	}

	dbSpecs := make([]DatabaseSpec, 0, len(dbs))
	for _, d := range dbs {
		dbSpecs = append(dbSpecs, DatabaseSpec{Name: d.Name, Owner: d.OwnerName})
	}
	return roleSpecs, dbSpecs, nil
}

// pushSpec 构建并推送 endpoint 的完整 spec；delta 只在本次 /configure 中下发。
// 成功后把不带 delta 的 spec 写回缓存并清除 pending 标记。
func pushSpec(ep *Endpoint, delta []DeltaOp) error {
	if ep == nil {
		return fmt.Errorf("endpoint 为空，无法推送 spec")
	}
	spec, err := buildSpecFromEndpoint(ep)
	if err != nil {
		return fmt.Errorf("构建 ComputeSpec 失败：%w", err)
	}
	spec.DeltaOperations = delta

	if err := configureCompute(context.Background(), ep.EndpointID, spec); err != nil {
		return fmt.Errorf("推送 compute 配置失败：%w", err)
	}

	spec.DeltaOperations = nil
	st.mu.Lock()
	ep.Spec = spec
	ep.PendingReconfigure = false
	st.mu.Unlock()
	return nil
}

// cloneCatalogToBranch 把父分支 catalog 复制到子分支（创建分支时使用）。
// 每个角色都会生成子分支独立的 Secret（同值），以便后续分支间独立轮换密码。
// 失败时清理本次已创建的子分支 Secret。
func cloneCatalogToBranch(parentBranchID, childBranchID string) (roles []*Role, dbs []*Database, err error) {
	parentRoles, parentDBs := catalogForBranch(parentBranchID)

	created := make([]string, 0, len(parentRoles))
	defer func() {
		if err == nil {
			return
		}
		for _, name := range created {
			_ = credStore.DeleteSecret(name, defaultCredentialSecretKey)
		}
	}()

	for _, r := range parentRoles {
		verifier, getErr := credStore.GetSecret(r.SecretName, r.SecretKey)
		if getErr != nil {
			err = fmt.Errorf("读取父分支角色 %s 的凭据：%w", r.Name, getErr)
			return nil, nil, err
		}
		secretName := roleSecretName(childBranchID, r.Name)
		if putErr := credStore.PutSecret(secretName, defaultCredentialSecretKey, verifier); putErr != nil {
			err = fmt.Errorf("复制角色 %s 的凭据：%w", r.Name, putErr)
			return nil, nil, err
		}
		created = append(created, secretName)
		now := nowRFC3339()
		roles = append(roles, &Role{
			BranchID:   childBranchID,
			Name:       r.Name,
			SecretName: secretName,
			SecretKey:  defaultCredentialSecretKey,
			Protected:  r.Protected,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}

	for _, d := range parentDBs {
		now := nowRFC3339()
		dbs = append(dbs, &Database{
			BranchID:  childBranchID,
			Name:      d.Name,
			OwnerName: d.OwnerName,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	return roles, dbs, nil
}
