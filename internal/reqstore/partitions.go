package reqstore

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Muxcore-Media/core/pkg/tenant"
)

// Partitions manages one SQLite database per tenant when TENANT_MODE=1,
// or a single shared DB when tenant mode is off.
type Partitions struct {
	root  string
	mu    sync.Mutex
	byKey map[string]*Store
}

// DBPath returns the SQLite file path for a tenant under root.
// TENANT_MODE=1 → {root}/tenants/{id}/requests.db; otherwise {root}/requests.db.
func DBPath(root, tenantID string) string {
	if !tenant.Enabled() {
		return filepath.Join(root, "requests.db")
	}
	return filepath.Join(tenant.DataDir(root, tenantID), "requests.db")
}

// OpenPartitions prepares a partition manager rooted at dir.
// When TENANT_MODE=1, opens any existing tenants/*/requests.db; stores are
// created on demand via ForTenant / Put.
func OpenPartitions(root string) (*Partitions, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data root: %w", err)
	}
	p := &Partitions{root: root, byKey: make(map[string]*Store)}
	if !tenant.Enabled() {
		st, err := Open(DBPath(root, ""))
		if err != nil {
			return nil, err
		}
		p.byKey[""] = st
		return p, nil
	}
	tenantsRoot := filepath.Join(root, "tenants")
	entries, err := os.ReadDir(tenantsRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read tenants dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := p.ForTenant(e.Name()); err != nil {
			_ = p.Close()
			return nil, fmt.Errorf("open tenant %q: %w", e.Name(), err)
		}
	}
	return p, nil
}

// PathFor returns the on-disk DB path for tenantID (for tests/ops).
func (p *Partitions) PathFor(tenantID string) string {
	return DBPath(p.root, tenantID)
}

// ForTenant returns (opening if needed) the store for tenantID.
func (p *Partitions) ForTenant(tenantID string) (*Store, error) {
	key := ""
	if tenant.Enabled() {
		key = tenant.OrDefault(tenantID)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, ok := p.byKey[key]; ok {
		return st, nil
	}
	path := DBPath(p.root, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir tenant partition: %w", err)
	}
	st, err := Open(path)
	if err != nil {
		return nil, err
	}
	p.byKey[key] = st
	return st, nil
}

// Put writes a record into the tenant's partition DB.
func (p *Partitions) Put(r *Record) error {
	if r == nil {
		return fmt.Errorf("nil record")
	}
	st, err := p.ForTenant(r.TenantID)
	if err != nil {
		return err
	}
	return st.Put(r)
}

// Get loads a request by ID from the given tenant's partition only.
// Cross-tenant lookups fail with "request not found".
func (p *Partitions) Get(id, tenantID string) (*Record, error) {
	st, err := p.ForTenant(tenantID)
	if err != nil {
		return nil, err
	}
	return st.Get(id)
}

// LoadAll merges records from every open (and discovered) partition.
func (p *Partitions) LoadAll() (map[string]*Record, error) {
	p.mu.Lock()
	keys := make([]string, 0, len(p.byKey))
	for k := range p.byKey {
		keys = append(keys, k)
	}
	p.mu.Unlock()

	out := make(map[string]*Record)
	for _, k := range keys {
		st, err := p.ForTenant(k)
		if err != nil {
			return nil, err
		}
		part, err := st.LoadAll()
		if err != nil {
			return nil, err
		}
		for id, r := range part {
			out[id] = r
		}
	}
	return out, nil
}

// Delete removes a request from the tenant's partition DB.
func (p *Partitions) Delete(id, tenantID string) error {
	if id == "" {
		return fmt.Errorf("id is required")
	}
	st, err := p.ForTenant(tenantID)
	if err != nil {
		return err
	}
	return st.Delete(id)
}

// Ping verifies every open partition database is reachable.
func (p *Partitions) Ping() error {
	p.mu.Lock()
	keys := make([]string, 0, len(p.byKey))
	for k := range p.byKey {
		keys = append(keys, k)
	}
	p.mu.Unlock()
	for _, k := range keys {
		st, err := p.ForTenant(k)
		if err != nil {
			return err
		}
		if err := st.Ping(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes all open tenant databases.
func (p *Partitions) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var first error
	for k, st := range p.byKey {
		if err := st.Close(); err != nil && first == nil {
			first = err
		}
		delete(p.byKey, k)
	}
	return first
}
