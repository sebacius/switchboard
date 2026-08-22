package filemanager

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
}

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

// ListTenants returns info about all tenant markdown files.
func (fm *FileManager) ListTenants() ([]TenantInfo, error) {
	entries, err := os.ReadDir(fm.tenantsDir)
	if err != nil {
		return nil, fmt.Errorf("read tenants dir: %w", err)
	}

	var tenants []TenantInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		tenants = append(tenants, TenantInfo{
			Name:     name,
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
	}
	return tenants, nil
}

// GetTenant reads a tenant markdown file by name (without .md extension).
func (fm *FileManager) GetTenant(name string) (string, error) {
	if err := validateTenantName(name); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(fm.tenantsDir, name+".md"))
	if err != nil {
		return "", fmt.Errorf("read tenant %s: %w", name, err)
	}
	return string(data), nil
}

// CreateTenant creates a new tenant markdown file. Fails if it already exists.
func (fm *FileManager) CreateTenant(name, content string) error {
	if err := validateTenantName(name); err != nil {
		return err
	}
	path := filepath.Join(fm.tenantsDir, name+".md")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("tenant %q already exists", name)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("create tenant %s: %w", name, err)
	}
	return nil
}

// PutTenant updates an existing tenant markdown file.
func (fm *FileManager) PutTenant(name, content string) error {
	if err := validateTenantName(name); err != nil {
		return err
	}
	path := filepath.Join(fm.tenantsDir, name+".md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("tenant %q not found", name)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("write tenant %s: %w", name, err)
	}
	return nil
}

// DeleteTenant removes a tenant markdown file.
func (fm *FileManager) DeleteTenant(name string) error {
	if err := validateTenantName(name); err != nil {
		return err
	}
	path := filepath.Join(fm.tenantsDir, name+".md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("tenant %q not found", name)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete tenant %s: %w", name, err)
	}
	return nil
}

// Reload refreshes the cached tenant prompts and routing tables. There is no
// dialplan to reload: a call is routed either by the tenant's routing table or
// by the supervisor, so those two files are the reloadable routing inputs.
func (fm *FileManager) Reload() error {
	var errors []string

	if fm.settingsReloader != nil {
		if err := fm.settingsReloader.ReloadSettings(); err != nil {
			errors = append(errors, fmt.Sprintf("prompts/routing: %v", err))
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
	// Strip .md suffix if provided
	name = strings.TrimSuffix(name, ".md")
	if !tenantNameRegex.MatchString(name) {
		return fmt.Errorf("tenant name %q contains invalid characters (use alphanumeric, hyphens, underscores, dots, ampersands)", name)
	}
	return nil
}
