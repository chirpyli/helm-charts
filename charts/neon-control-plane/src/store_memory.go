package main

import (
	"context"
	"sort"
	"sync"
)

// =============================================================================
// MemoryStateStore：单测 / 本地调试用的纯内存实现。
// 明确语义：进程重启后状态全部丢失，不用于集群内部署。
// =============================================================================

type memoryStateStore struct {
	mu          sync.RWMutex
	projects    map[string]*Project
	branches    map[string]*Branch
	endpoints   map[string]*Endpoint
	credentials map[string]*EndpointCredential
	roles       map[string]*Role
	databases   map[string]*Database
}

func newMemoryStateStore() StateStore {
	return &memoryStateStore{
		projects:    map[string]*Project{},
		branches:    map[string]*Branch{},
		endpoints:   map[string]*Endpoint{},
		credentials: map[string]*EndpointCredential{},
		roles:       map[string]*Role{},
		databases:   map[string]*Database{},
	}
}

func (s *memoryStateStore) Migrate(context.Context) error { return nil }

func (s *memoryStateStore) Load(context.Context) (*StateSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := &StateSnapshot{
		Projects:    map[string]*Project{},
		Branches:    map[string]*Branch{},
		Endpoints:   map[string]*Endpoint{},
		Credentials: map[string]*EndpointCredential{},
	}
	for k, v := range s.projects {
		snap.Projects[k] = v
	}
	for k, v := range s.branches {
		snap.Branches[k] = v
	}
	for k, v := range s.endpoints {
		snap.Endpoints[k] = v
	}
	for k, v := range s.credentials {
		snap.Credentials[k] = v
	}
	for _, r := range s.roles {
		snap.Roles = append(snap.Roles, r)
	}
	for _, d := range s.databases {
		snap.Databases = append(snap.Databases, d)
	}
	return snap, nil
}

func (s *memoryStateStore) CreateProjectBundle(_ context.Context, b *ProjectBundle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.Project != nil {
		s.projects[b.Project.ProjectID] = b.Project
	}
	if b.Branch != nil {
		s.branches[b.Branch.BranchID] = b.Branch
	}
	for _, r := range b.Roles {
		s.roles[catalogKey(r.BranchID, r.Name)] = r
	}
	for _, d := range b.Databases {
		s.databases[catalogKey(d.BranchID, d.Name)] = d
	}
	if b.Endpoint != nil {
		s.endpoints[b.Endpoint.EndpointID] = b.Endpoint
	}
	if b.Credential != nil {
		s.credentials[b.Credential.EndpointID] = b.Credential
	}
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) UpsertBranch(_ context.Context, b *Branch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.branches[b.BranchID] = b
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) DeleteBranch(_ context.Context, branchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.branches, branchID)
	for k, r := range s.roles {
		if r.BranchID == branchID {
			delete(s.roles, k)
		}
	}
	for k, d := range s.databases {
		if d.BranchID == branchID {
			delete(s.databases, k)
		}
	}
	for id, ep := range s.endpoints {
		if ep != nil && ep.BranchID == branchID {
			delete(s.endpoints, id)
			delete(s.credentials, id)
		}
	}
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) UpsertEndpoint(_ context.Context, ep *Endpoint, cred *EndpointCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endpoints[ep.EndpointID] = ep
	if cred != nil {
		s.credentials[cred.EndpointID] = cred
	}
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) DeleteEndpoint(_ context.Context, endpointID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.endpoints, endpointID)
	delete(s.credentials, endpointID)
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) DeleteProject(_ context.Context, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.projects, projectID)
	for id, b := range s.branches {
		if b != nil && b.ProjectID == projectID {
			delete(s.branches, id)
			for k, r := range s.roles {
				if r.BranchID == id {
					delete(s.roles, k)
				}
			}
			for k, d := range s.databases {
				if d.BranchID == id {
					delete(s.databases, k)
				}
			}
		}
	}
	for id, ep := range s.endpoints {
		if ep != nil && ep.ProjectID == projectID {
			delete(s.endpoints, id)
			delete(s.credentials, id)
		}
	}
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) UpdateRecoveryState(_ context.Context, endpointID string, patch RecoveryPatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ep, ok := s.endpoints[endpointID]; ok && ep != nil {
		if patch.RouteFingerprint != nil {
			ep.RouteFingerprint = *patch.RouteFingerprint
		}
		if patch.SafekeeperGeneration != nil {
			ep.SafekeeperGeneration = *patch.SafekeeperGeneration
		}
		if patch.PendingReconfigure != nil {
			ep.PendingReconfigure = *patch.PendingReconfigure
		}
	}
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) UpdateObservedStatus(_ context.Context, updates []StatusUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range updates {
		if ep, ok := s.endpoints[u.EndpointID]; ok && ep != nil {
			ep.Status = u.Status
		}
	}
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) ResolveCredentials(_ context.Context, ep *Endpoint) (string, error) {
	return credStore.Get(ep)
}

func (s *memoryStateStore) Health(context.Context) error { return nil }
func (s *memoryStateStore) Flush(context.Context) error  { return nil }
func (s *memoryStateStore) Close(context.Context) error  { return nil }

// UpsertBranchCatalog 内存实现：branch 与 roles/databases 一次性写入。
func (s *memoryStateStore) UpsertBranchCatalog(_ context.Context, b *Branch, roles []*Role, dbs []*Database) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.branches[b.BranchID] = b
	for _, r := range roles {
		s.roles[catalogKey(r.BranchID, r.Name)] = r
	}
	for _, d := range dbs {
		s.databases[catalogKey(d.BranchID, d.Name)] = d
	}
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) ListRoles(_ context.Context, branchID string) ([]*Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Role, 0)
	for _, r := range s.roles {
		if r.BranchID == branchID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memoryStateStore) GetRole(_ context.Context, branchID, name string) (*Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.roles[catalogKey(branchID, name)], nil
}

func (s *memoryStateStore) UpsertRole(_ context.Context, r *Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[catalogKey(r.BranchID, r.Name)] = r
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) DeleteRole(_ context.Context, branchID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.roles, catalogKey(branchID, name))
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) ListDatabases(_ context.Context, branchID string) ([]*Database, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Database, 0)
	for _, d := range s.databases {
		if d.BranchID == branchID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memoryStateStore) GetDatabase(_ context.Context, branchID, name string) (*Database, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.databases[catalogKey(branchID, name)], nil
}

func (s *memoryStateStore) UpsertDatabase(_ context.Context, d *Database) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.databases[catalogKey(d.BranchID, d.Name)] = d
	recordStoreWrite(nil)
	return nil
}

func (s *memoryStateStore) DeleteDatabase(_ context.Context, branchID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.databases, catalogKey(branchID, name))
	recordStoreWrite(nil)
	return nil
}
