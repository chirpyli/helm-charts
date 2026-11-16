package main

import (
	"log"
	"sort"
)

// =============================================================================
// 启动时 catalog 回填（幂等）
//
// 背景：roles/databases 是 0003 迁移新增的 desired state。升级前创建的
// project/branch 只有 endpoint_credentials（单角色/单库），需要在启动时补齐：
//   1. 根分支（main）从 endpoint_credentials 回填初始角色/数据库；
//   2. 子分支从父分支复制（verifier 同值，但写入独立 Secret）。
//
// 失败只记录 WARN，不阻塞启动：目录接口在缺少数据时会返回 404/409，
// 但不影响既有 project/branch/endpoint 的读写。
// =============================================================================

// seedCatalogRegistry 按 parent_id 拓扑序补齐 roles/databases。
func seedCatalogRegistry() {
	st.mu.RLock()
	branches := make([]*Branch, 0, len(st.branches))
	for _, b := range st.branches {
		if b != nil {
			branches = append(branches, b)
		}
	}
	endpoints := make([]*Endpoint, 0, len(st.endpoints))
	for _, ep := range st.endpoints {
		if ep != nil {
			endpoints = append(endpoints, ep)
		}
	}
	st.mu.RUnlock()

	if len(branches) == 0 {
		return
	}

	byID := make(map[string]*Branch, len(branches))
	for _, b := range branches {
		byID[b.BranchID] = b
	}
	sort.Slice(branches, func(i, j int) bool {
		di, dj := branchDepth(branches[i], byID), branchDepth(branches[j], byID)
		if di != dj {
			return di < dj
		}
		return branches[i].BranchID < branches[j].BranchID
	})

	seeded := 0
	for _, b := range branches {
		existingRoles, existingDBs := catalogForBranch(b.BranchID)
		if len(existingRoles) > 0 || len(existingDBs) > 0 {
			continue
		}

		var roles []*Role
		var dbs []*Database
		var err error

		if b.ParentID != "" {
			parentRoles, parentDBs := catalogForBranch(b.ParentID)
			if len(parentRoles) == 0 && len(parentDBs) == 0 {
				// 父分支也没有数据：留待人工/后续版本处理。
				continue
			}
			roles, dbs, err = cloneCatalogToBranch(b.ParentID, b.BranchID)
		} else {
			roles, dbs, err = seedCatalogFromEndpoint(b, endpoints)
		}
		if err != nil {
			logWarn("seedCatalogRegistry %s: %v", b.BranchID, err)
			continue
		}
		if len(roles) == 0 && len(dbs) == 0 {
			continue
		}
		if err := storeUpsertBranchCatalog(b, roles, dbs); err != nil {
			logWarn("seedCatalogRegistry %s: 持久化失败：%v", b.BranchID, err)
			for _, r := range roles {
				_ = credStore.DeleteSecret(r.SecretName, r.SecretKey)
			}
			continue
		}

		st.mu.Lock()
		st.ensureCatalogMapsLocked()
		for _, r := range roles {
			st.roles[catalogKey(r.BranchID, r.Name)] = r
		}
		for _, d := range dbs {
			st.databases[catalogKey(d.BranchID, d.Name)] = d
		}
		st.mu.Unlock()
		seeded++
	}

	if seeded > 0 {
		log.Printf("seedCatalogRegistry: 已回填 %d 个分支的角色/数据库目录", seeded)
	}
}

// branchDepth 计算分支深度（根为 0），用于拓扑序处理。
func branchDepth(b *Branch, byID map[string]*Branch) int {
	depth := 0
	seen := map[string]bool{}
	for cur := b; cur != nil && cur.ParentID != "" && !seen[cur.ParentID]; {
		seen[cur.ParentID] = true
		parent, ok := byID[cur.ParentID]
		if !ok {
			break
		}
		depth++
		cur = parent
	}
	return depth
}

// seedCatalogFromEndpoint 用根分支上已有 endpoint 的默认身份回填 catalog。
// verifier 从 endpoint Secret 复制到角色 Secret，保证后续按角色独立轮换。
func seedCatalogFromEndpoint(b *Branch, endpoints []*Endpoint) ([]*Role, []*Database, error) {
	candidates := make([]*Endpoint, 0)
	for _, ep := range endpoints {
		if ep.BranchID == b.BranchID && ep.RoleName != "" && ep.DatabaseName != "" {
			candidates = append(candidates, ep)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		ri := candidates[i].Type == "" || candidates[i].Type == EndpointTypeReadWrite
		rj := candidates[j].Type == "" || candidates[j].Type == EndpointTypeReadWrite
		if ri != rj {
			return ri
		}
		return candidates[i].EndpointID < candidates[j].EndpointID
	})
	if len(candidates) == 0 {
		return nil, nil, nil
	}

	ep := candidates[0]
	verifier, err := credStore.Get(ep)
	if err != nil {
		return nil, nil, err
	}
	secretName := roleSecretName(b.BranchID, ep.RoleName)
	if err := credStore.PutSecret(secretName, defaultCredentialSecretKey, verifier); err != nil {
		return nil, nil, err
	}

	owner := ep.Owner
	if owner == "" {
		owner = ep.RoleName
	}
	now := nowRFC3339()
	return []*Role{{
			BranchID:   b.BranchID,
			Name:       ep.RoleName,
			SecretName: secretName,
			SecretKey:  defaultCredentialSecretKey,
			Protected:  true,
			CreatedAt:  now,
			UpdatedAt:  now,
		}}, []*Database{{
			BranchID:  b.BranchID,
			Name:      ep.DatabaseName,
			OwnerName: owner,
			CreatedAt: now,
			UpdatedAt: now,
		}}, nil
}
