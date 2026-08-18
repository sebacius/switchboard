package filemanager

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SettingsReloader refreshes the cached prompts an edit through this API
// affects. The agent's PromptStore implements it, so writing a tenant file or
// settings.md takes effect on the NEXT call without a restart — calls already in
// flight keep the prompt they were admitted with.
type SettingsReloader interface {
	ReloadSettings() error
}

// TenantInfo describes a tenant markdown file.
type TenantInfo struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Config holds paths and dependencies for the FileManager.
type Config struct {
	SettingsDir      string           // directory containing settings.md
	TenantsDir       string           // directory containing tenant .md files
	SettingsReloader SettingsReloader // optional, for refreshing cached prompts
}

// FileManager provides safe file operations for settings and tenant prompts.
type FileManager struct {
	settingsDir      string
	tenantsDir       string
	settingsReloader SettingsReloader
}

var tenantNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-\.&]*$`)

// New creates a new FileManager.
func New(cfg Config) *FileManager {
	return &FileManager{
		settingsDir:      cfg.SettingsDir,
		tenantsDir:       cfg.TenantsDir,
		settingsReloader: cfg.SettingsReloader,
	}
}

// GetSettings reads the settings.md file.
func (fm *FileManager) GetSettings() (string, error) {
	data, err := os.ReadFile(filepath.Join(fm.settingsDir, "settings.md"))
	if err != nil {
		return "", fmt.Errorf("read settings: %w", err)
	}
	return string(data), nil
}

// PutSettings writes the settings.md file.
func (fm *FileManager) PutSettings(content string) error {
	path := filepath.Join(fm.settingsDir, "settings.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
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

// Reload refreshes the cached tenant prompts. There is no dialplan to reload:
// routing decisions are made per call by the supervisor, so a prompt edit is
// the only reloadable routing input.
func (fm *FileManager) Reload() error {
	var errors []string

	if fm.settingsReloader != nil {
		if err := fm.settingsReloader.ReloadSettings(); err != nil {
			errors = append(errors, fmt.Sprintf("prompts: %v", err))
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
