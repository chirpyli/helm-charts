package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// =============================================================================
// Roles / Databases API（分支级目录）
//
// 写入策略（docs/dev/control-plane-api-step1-step2.md）：
//   - 创建：写 Secret + registry 行 → 构建全量 spec → /configure；失败回滚；
//   - 重置密码：备份旧 verifier → 写新 verifier → /configure；失败恢复；
//   - 删除/改名：先 /configure（delta_operations，IF EXISTS 幂等）→ 再改库。
// 明文密码只在创建/reset 的响应中出现一次，绝不写入内存 registry。
// =============================================================================

func catalogOperations() []map[string]string {
	return []map[string]string{{"action": "apply_config", "status": "finished"}}
}

func decodeJSONBody(r *http.Request, dst interface{}) error {
	if r.Body == nil {
		return fmt.Errorf("请求体为空")
	}
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return fmt.Errorf("请求体不是合法 JSON：%w", err)
	}
	return nil
}

func roleExistsLocked(branchID, name string) *Role {
	if st.roles == nil {
		return nil
	}
	return st.roles[catalogKey(branchID, name)]
}

func databaseExistsLocked(branchID, name string) *Database {
	if st.databases == nil {
		return nil
	}
	return st.databases[catalogKey(branchID, name)]
}

func putRoleMemory(r *Role) {
	st.mu.Lock()
	st.ensureCatalogMapsLocked()
	st.roles[catalogKey(r.BranchID, r.Name)] = r
	st.mu.Unlock()
}

func deleteRoleMemory(branchID, name string) {
	st.mu.Lock()
	delete(st.roles, catalogKey(branchID, name))
	st.mu.Unlock()
}

func putDatabaseMemory(d *Database) {
	st.mu.Lock()
	st.ensureCatalogMapsLocked()
	st.databases[catalogKey(d.BranchID, d.Name)] = d
	st.mu.Unlock()
}

func deleteDatabaseMemory(branchID, name string) {
	st.mu.Lock()
	delete(st.databases, catalogKey(branchID, name))
	st.mu.Unlock()
}

// -----------------------------------------------------------------------------
// Roles
// -----------------------------------------------------------------------------

func createRole(w http.ResponseWriter, r *http.Request, projectID, branchID string) {
	var req RoleCreateRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(req.Role.Name)
	if name == "" || !validPgIdent(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "role.name 必须是合法的 Postgres 标识符（≤63 字节，字母或下划线开头）",
		})
		return
	}
	if req.Role.NoLogin {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "当前 compute 版本不支持 no_login（NOLOGIN 会被强制覆盖为 LOGIN）",
		})
		return
	}
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}

	unlock := lockBranch(branchID)
	defer unlock()

	st.mu.RLock()
	existing := roleExistsLocked(branchID, name)
	st.mu.RUnlock()
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "role already exists"})
		return
	}

	ep, err := requireWritableEndpoint(branchID)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	password := randHex(16)
	verifier, err := generateScramVerifier(password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "生成 SCRAM 验证器失败"})
		return
	}
	now := nowRFC3339()
	role := &Role{
		BranchID:   branchID,
		Name:       name,
		SecretName: roleSecretName(branchID, name),
		SecretKey:  defaultCredentialSecretKey,
		Protected:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := credStore.PutSecret(role.SecretName, role.SecretKey, verifier); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := getStore().UpsertRole(context.Background(), role); err != nil {
		_ = credStore.DeleteSecret(role.SecretName, role.SecretKey)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	putRoleMemory(role)

	if err := pushSpec(ep, nil); err != nil {
		// 回滚：删行 + 删 Secret + 内存。
		_ = getStore().DeleteRole(context.Background(), branchID, name)
		_ = credStore.DeleteSecret(role.SecretName, role.SecretKey)
		deleteRoleMemory(branchID, name)
		logWarn("createRole %s/%s: %v", branchID, name, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	// 明文密码只放在响应副本里，registry 中的 Role 永远不带 Password。
	respRole := *role
	respRole.Password = password
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"role":       &respRole,
		"operations": catalogOperations(),
	})
}

func listRoles(w http.ResponseWriter, r *http.Request, projectID, branchID string) {
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}
	roles, _ := catalogForBranch(branchID)
	out := make([]Role, 0, len(roles))
	for _, role := range roles {
		role.Password = ""
		out = append(out, role)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"roles": out})
}

func getRole(w http.ResponseWriter, r *http.Request, projectID, branchID, name string) {
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}
	st.mu.RLock()
	role := roleExistsLocked(branchID, name)
	var copyRole Role
	if role != nil {
		copyRole = *role
	}
	st.mu.RUnlock()
	if role == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "role not found"})
		return
	}
	copyRole.Password = ""
	writeJSON(w, http.StatusOK, map[string]interface{}{"role": &copyRole})
}

func deleteRole(w http.ResponseWriter, r *http.Request, projectID, branchID, name string) {
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}
	unlock := lockBranch(branchID)
	defer unlock()

	st.mu.RLock()
	role := roleExistsLocked(branchID, name)
	st.mu.RUnlock()
	if role == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "role not found"})
		return
	}
	if role.Protected {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "protected role cannot be deleted"})
		return
	}

	// 角色仍拥有数据库时禁止删除（与 Postgres 约束一致）。
	_, dbs := catalogForBranch(branchID)
	owned := make([]string, 0)
	for _, d := range dbs {
		if d.OwnerName == name {
			owned = append(owned, d.Name)
		}
	}
	if len(owned) > 0 {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":     "role still owns databases",
			"databases": owned,
		})
		return
	}

	ep, err := requireWritableEndpoint(branchID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err := pushSpec(ep, []DeltaOp{{Action: "delete_role", Name: name}}); err != nil {
		logWarn("deleteRole %s/%s: %v", branchID, name, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if err := getStore().DeleteRole(context.Background(), branchID, name); err != nil {
		queueCleanup("db", "role:"+branchID+"/"+name, err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := credStore.DeleteSecret(role.SecretName, role.SecretKey); err != nil {
		queueCleanup("secret", role.SecretName, err.Error())
	}
	deleteRoleMemory(branchID, name)

	respRole := *role
	respRole.Password = ""
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"role":       &respRole,
		"operations": catalogOperations(),
	})
}

func resetRolePassword(w http.ResponseWriter, r *http.Request, projectID, branchID, name string) {
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}
	unlock := lockBranch(branchID)
	defer unlock()

	st.mu.RLock()
	role := roleExistsLocked(branchID, name)
	st.mu.RUnlock()
	if role == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "role not found"})
		return
	}
	if role.Protected {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "protected role cannot reset password"})
		return
	}

	ep, err := requireWritableEndpoint(branchID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	oldVerifier, err := credStore.GetSecret(role.SecretName, role.SecretKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	password := randHex(16)
	verifier, err := generateScramVerifier(password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "生成 SCRAM 验证器失败"})
		return
	}
	if err := credStore.PutSecret(role.SecretName, role.SecretKey, verifier); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := pushSpec(ep, nil); err != nil {
		// 恢复旧 verifier，避免控制面与 compute 长期不一致。
		if restoreErr := credStore.PutSecret(role.SecretName, role.SecretKey, oldVerifier); restoreErr != nil {
			queueCleanup("secret", role.SecretName, restoreErr.Error())
		}
		logWarn("resetRolePassword %s/%s: %v", branchID, name, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	respRole := *role
	respRole.Password = password
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"role":       &respRole,
		"operations": catalogOperations(),
	})
}

func revealRolePassword(w http.ResponseWriter, r *http.Request, projectID, branchID, name string) {
	// 控制面只保存 SCRAM verifier，不保存明文；与官方差异在 README 中说明。
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "控制面不保存明文密码，请使用 reset_password",
	})
}

// -----------------------------------------------------------------------------
// Databases
// -----------------------------------------------------------------------------

func createDatabase(w http.ResponseWriter, r *http.Request, projectID, branchID string) {
	var req DatabaseCreateRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(req.Database.Name)
	owner := strings.TrimSpace(req.Database.OwnerName)
	if name == "" || !validPgIdent(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "database.name 必须是合法的 Postgres 标识符（≤63 字节）",
		})
		return
	}
	if owner == "" || !validPgIdent(owner) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "database.owner_name 必填且必须是合法标识符"})
		return
	}
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}

	unlock := lockBranch(branchID)
	defer unlock()

	st.mu.RLock()
	existing := databaseExistsLocked(branchID, name)
	ownerExists := roleExistsLocked(branchID, owner)
	st.mu.RUnlock()
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "database already exists"})
		return
	}
	if ownerExists == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner role does not exist in this branch"})
		return
	}

	ep, err := requireWritableEndpoint(branchID)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	now := nowRFC3339()
	db := &Database{BranchID: branchID, Name: name, OwnerName: owner, CreatedAt: now, UpdatedAt: now}
	if err := getStore().UpsertDatabase(context.Background(), db); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	putDatabaseMemory(db)

	if err := pushSpec(ep, nil); err != nil {
		_ = getStore().DeleteDatabase(context.Background(), branchID, name)
		deleteDatabaseMemory(branchID, name)
		logWarn("createDatabase %s/%s: %v", branchID, name, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"database":   db,
		"operations": catalogOperations(),
	})
}

func listDatabases(w http.ResponseWriter, r *http.Request, projectID, branchID string) {
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}
	_, dbs := catalogForBranch(branchID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"databases": dbs})
}

func getDatabase(w http.ResponseWriter, r *http.Request, projectID, branchID, name string) {
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}
	st.mu.RLock()
	db := databaseExistsLocked(branchID, name)
	st.mu.RUnlock()
	if db == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "database not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"database": db})
}

func updateDatabase(w http.ResponseWriter, r *http.Request, projectID, branchID, name string) {
	var req DatabaseUpdateRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}

	unlock := lockBranch(branchID)
	defer unlock()

	st.mu.RLock()
	db := databaseExistsLocked(branchID, name)
	st.mu.RUnlock()
	if db == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "database not found"})
		return
	}

	newName := strings.TrimSpace(req.Database.Name)
	if newName == "" {
		newName = name
	}
	if !validPgIdent(newName) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "database.name 不是合法标识符"})
		return
	}
	newOwner := strings.TrimSpace(req.Database.OwnerName)
	if newOwner == "" {
		newOwner = db.OwnerName
	}
	if !validPgIdent(newOwner) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "database.owner_name 不是合法标识符"})
		return
	}

	st.mu.RLock()
	conflict := databaseExistsLocked(branchID, newName)
	ownerExists := roleExistsLocked(branchID, newOwner)
	st.mu.RUnlock()
	if newName != name && conflict != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "target database name already exists"})
		return
	}
	if ownerExists == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner role does not exist in this branch"})
		return
	}

	ep, err := requireWritableEndpoint(branchID)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	old := *db
	updated := &Database{
		BranchID: branchID, Name: newName, OwnerName: newOwner,
		CreatedAt: old.CreatedAt, UpdatedAt: nowRFC3339(),
	}
	if err := getStore().UpsertDatabase(context.Background(), updated); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if newName != name {
		_ = getStore().DeleteDatabase(context.Background(), branchID, name)
	}
	// 内存：先删旧键再写新键，避免 PATCH 改名后残留旧记录。
	deleteDatabaseMemory(branchID, name)
	putDatabaseMemory(updated)

	var delta []DeltaOp
	if newName != name {
		delta = []DeltaOp{{Action: "rename_db", Name: name, NewName: newName}}
	}
	if err := pushSpec(ep, delta); err != nil {
		// 回滚 registry 与内存。
		_ = getStore().DeleteDatabase(context.Background(), branchID, newName)
		_ = getStore().UpsertDatabase(context.Background(), &old)
		deleteDatabaseMemory(branchID, newName)
		putDatabaseMemory(&old)
		logWarn("updateDatabase %s/%s: %v", branchID, name, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"database":   updated,
		"operations": catalogOperations(),
	})
}

func deleteDatabase(w http.ResponseWriter, r *http.Request, projectID, branchID, name string) {
	if _, err := requireBranch(projectID, branchID); err != nil {
		writeAPIError(w, err)
		return
	}
	unlock := lockBranch(branchID)
	defer unlock()

	st.mu.RLock()
	db := databaseExistsLocked(branchID, name)
	st.mu.RUnlock()
	if db == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "database not found"})
		return
	}

	ep, err := requireWritableEndpoint(branchID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err := pushSpec(ep, []DeltaOp{{Action: "delete_db", Name: name}}); err != nil {
		logWarn("deleteDatabase %s/%s: %v", branchID, name, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if err := getStore().DeleteDatabase(context.Background(), branchID, name); err != nil {
		queueCleanup("db", "database:"+branchID+"/"+name, err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	deleteDatabaseMemory(branchID, name)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"database":   db,
		"operations": catalogOperations(),
	})
}
