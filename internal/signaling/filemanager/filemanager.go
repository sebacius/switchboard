package filemanager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// SettingsReloader refreshes the cached configuration an edit through this API
// affects. The routing store implements it, so writing a tenant file takes
// effect on the NEXT call without a restart — calls already in flight keep the
// configuration they were admitted with.
type SettingsReloader interface {
	ReloadSettings() error
}

// MultiReloader fans one reload out to several reloaders, reporting every
// failure rather than the first. It is kept for deployments that register more
// than one store behind the config API.
type MultiReloader []SettingsReloader

// ReloadSettings reloads every member, accumulating errors so one failure does
// not hide another behind it.
func (m MultiReloader) ReloadSettings() error {
	var errs []string
	for _, r := range m {
		if r == nil {
			continue
		}
		if err := r.ReloadSettings(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// TenantInfo describes a tenant's configuration files.
type TenantInfo struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// HasFlows reports whether the tenant has a flow graph as well as a routing
	// table. Flows are optional — a tenant may route entirely by direct mapping
	// — so the list has to say which kind of tenant this is.
	HasFlows bool `json:"has_flows"`
}

// FileKind names which of a tenant's files an operation addresses.
type FileKind string

const (
	// KindRouting is <tenant>.routing.json: the entry mapping, extensions, ring
	// groups and symbolic targets.
	KindRouting FileKind = "routing"
	// KindFlows is <tenant>.flows.json: the graphs.
	KindFlows FileKind = "flows"
)

// suffix returns the filename suffix for a kind.
func (k FileKind) suffix() (string, error) {
	switch k {
	case KindRouting:
		return routingSuffix, nil
	case KindFlows:
		return flowsSuffix, nil
	default:
		return "", fmt.Errorf("unknown file kind %q (want %q or %q)", k, KindRouting, KindFlows)
	}
}

// The two files that describe a tenant. The prompts these replaced were prose,
// so an editor could not do better than save what it was given; a flow graph is
// far more error-prone to hand-edit, which is why writes are validated.
const (
	routingSuffix = ".routing.json"
	flowsSuffix   = ".flows.json"
)

// Config holds paths and dependencies for the FileManager.
type Config struct {
	TenantsDir       string           // directory containing per-tenant configuration files
	SettingsReloader SettingsReloader // optional, for refreshing cached configuration
}

// FileManager provides safe file operations for tenant configuration.
type FileManager struct {
	tenantsDir       string
	settingsReloader SettingsReloader
}

var tenantNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-\.&]*$`)

// New creates a new FileManager.
func New(cfg Config) *FileManager {
	return &FileManager{
		tenantsDir:       cfg.TenantsDir,
		settingsReloader: cfg.SettingsReloader,
	}
}

// ErrNotFound and ErrAlreadyExists let a caller classify a failure without
// matching on message text. The API maps them to 404 and 409, and a reworded
// message must not silently turn one status into another.
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
)

// writeFileAtomic writes through a temp file in the SAME directory and renames.
//
// A configuration file is read by a reloader that can run at any moment, so a
// truncate-then-write leaves a window in which a tenant has an empty routing
// table. Rename within a directory is atomic, so a reader sees either the old
// file or the new one, and a crash mid-write leaves the old one intact.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename succeeds

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	return nil
}

// ListTenants returns every tenant that has configuration on disk.
func (fm *FileManager) ListTenants() ([]TenantInfo, error) {
	entries, err := os.ReadDir(fm.tenantsDir)
	if err != nil {
		return nil, fmt.Errorf("read tenants dir: %w", err)
	}

	seen := map[string]*TenantInfo{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		var tenant string
		var isFlows bool
		switch {
		case strings.HasSuffix(name, routingSuffix):
			tenant = strings.TrimSuffix(name, routingSuffix)
		case strings.HasSuffix(name, flowsSuffix):
			tenant, isFlows = strings.TrimSuffix(name, flowsSuffix), true
		default:
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		t, ok := seen[tenant]
		if !ok {
			t = &TenantInfo{Name: tenant}
			seen[tenant] = t
		}
		t.Size += info.Size()
		if info.ModTime().After(t.Modified) {
			t.Modified = info.ModTime()
		}
		if isFlows {
			t.HasFlows = true
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)

	tenants := make([]TenantInfo, 0, len(names))
	for _, name := range names {
		tenants = append(tenants, *seen[name])
	}
	return tenants, nil
}

// GetTenantFile reads one of a tenant's configuration files.
func (fm *FileManager) GetTenantFile(name string, kind FileKind) (string, error) {
	path, err := fm.pathFor(name, kind)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && kind == KindFlows {
			// Flows are optional; an absent file is an empty graph set rather
			// than an error, so an editor can create one by saving.
			return "", nil
		}
		return "", fmt.Errorf("read %s for tenant %s: %w", kind, name, err)
	}
	return string(data), nil
}

// GetTenant reads a tenant's routing file.
func (fm *FileManager) GetTenant(name string) (string, error) {
	return fm.GetTenantFile(name, KindRouting)
}

// PutTenantFile validates and writes one of a tenant's files.
//
// The write is REJECTED if the result would not load. A prompt could not be
// wrong in a way that broke routing; a flow graph can, and saving one that the
// server would then refuse to reload would take the tenant's routing down at
// the next restart. So the candidate is validated against the tenant's other
// file first, and nothing touches disk unless it passes.
//
// Only ERRORS block the write. A warning is a finding the loader would accept —
// refusing it would make the editor stricter than the server, so warnings are
// returned alongside a successful save for the operator to read.
func (fm *FileManager) PutTenantFile(name string, kind FileKind, content string) (dialplan.Problems, error) {
	path, err := fm.pathFor(name, kind)
	if err != nil {
		return nil, err
	}

	problems := fm.validateCandidate(name, kind, content)
	if problems.HasErrors() {
		return nil, &ValidationError{Tenant: name, Problems: problems}
	}

	if err := writeFileAtomic(path, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write %s for tenant %s: %w", kind, name, err)
	}
	return problems, nil
}

// PutTenant updates a tenant's routing file.
func (fm *FileManager) PutTenant(name, content string) (dialplan.Problems, error) {
	return fm.PutTenantFile(name, KindRouting, content)
}

// CreateTenant creates a tenant's routing file. Fails if it already exists.
func (fm *FileManager) CreateTenant(name, content string) (dialplan.Problems, error) {
	path, err := fm.pathFor(name, KindRouting)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("tenant %q: %w", name, ErrAlreadyExists)
	}
	return fm.PutTenantFile(name, KindRouting, content)
}

// DeleteTenant removes every file belonging to a tenant.
func (fm *FileManager) DeleteTenant(name string) error {
	if err := validateTenantName(name); err != nil {
		return err
	}

	routing := filepath.Join(fm.tenantsDir, name+routingSuffix)
	if _, err := os.Stat(routing); os.IsNotExist(err) {
		return fmt.Errorf("tenant %q: %w", name, ErrNotFound)
	}
	if err := os.Remove(routing); err != nil {
		return fmt.Errorf("delete tenant %s: %w", name, err)
	}
	// Leaving an orphaned flow file behind would make the tenant fail to load
	// on the next restart, which is a worse outcome than deleting it.
	flows := filepath.Join(fm.tenantsDir, name+flowsSuffix)
	if _, err := os.Stat(flows); err == nil {
		if err := os.Remove(flows); err != nil {
			return fmt.Errorf("delete flows for tenant %s: %w", name, err)
		}
	}
	return nil
}

// pathFor resolves a tenant file path, rejecting an unsafe name.
func (fm *FileManager) pathFor(name string, kind FileKind) (string, error) {
	if err := validateTenantName(name); err != nil {
		return "", err
	}
	suffix, err := kind.suffix()
	if err != nil {
		return "", err
	}
	return filepath.Join(fm.tenantsDir, name+suffix), nil
}

// Reload refreshes the cached routing tables and flows.
func (fm *FileManager) Reload() error {
	var errors []string

	if fm.settingsReloader != nil {
		if err := fm.settingsReloader.ReloadSettings(); err != nil {
			errors = append(errors, fmt.Sprintf("routing: %v", err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("reload errors: %s", strings.Join(errors, "; "))
	}
	return nil
}

// validateTenantName checks that a tenant name is safe for use as a filename.
func validateTenantName(name string) error {
	if name == "" {
		return fmt.Errorf("tenant name is required")
	}
	if len(name) > 100 {
		return fmt.Errorf("tenant name too long (max 100 characters)")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return fmt.Errorf("tenant name contains invalid characters")
	}
	if !tenantNameRegex.MatchString(name) {
		return fmt.Errorf("tenant name %q contains invalid characters (use alphanumeric, hyphens, underscores, dots, ampersands)", name)
	}
	return nil
}
