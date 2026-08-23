package filemanager

import (
	"fmt"
	"os"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// The deployment-wide files, as opposed to the per-tenant pair. They differ from
// a tenant file in two ways that the API has to carry: there is a fixed set of
// them, so the wire takes a NAME and never a path; and none of them can be
// reloaded, so saving one is only half of activating it.

// GlobalKind names one of the deployment-wide configuration files.
type GlobalKind string

const (
	// KindPolicy is policy.json: Class of Service, channel limits, spend caps.
	KindPolicy GlobalKind = "policy"
	// KindRoutes is routes.json: the DID -> tenant table.
	KindRoutes GlobalKind = "routes"
	// KindTrunkPeers is trunk_peers.json: the static SIP trunk peers.
	KindTrunkPeers GlobalKind = "trunk_peers"
)

// GlobalKinds is the allowlist, in display order.
var GlobalKinds = []GlobalKind{KindPolicy, KindRoutes, KindTrunkPeers}

// ParseGlobalKind resolves a name from the wire against the allowlist. An
// unknown name is not a path — it is simply not a file this API addresses.
func ParseGlobalKind(name string) (GlobalKind, bool) {
	for _, k := range GlobalKinds {
		if string(k) == name {
			return k, true
		}
	}
	return "", false
}

// Activation says what it takes for a saved file to take effect.
type Activation string

const (
	// ActivationReload is activated by POST /api/v1/config/reload.
	ActivationReload Activation = "reload"
	// ActivationRestart needs the process restarted. The policy overrides, the
	// DID table and the peer store are all captured by value at construction, so
	// a reload cannot swap them and pretending otherwise would leave an operator
	// believing an edit is live when it is not.
	ActivationRestart Activation = "restart"
)

// GlobalFileInfo describes one deployment-wide file.
type GlobalFileInfo struct {
	Name     GlobalKind `json:"name"`
	Path     string     `json:"path"`
	Size     int64      `json:"size"`
	Modified time.Time  `json:"modified"`
	// Exists reports whether the file is on disk. Absent is legal for all three
	// — each loader falls back to a safe default — so it is a state, not an error.
	Exists bool `json:"exists"`
	// Configured reports whether this server was given a path for the file at
	// all. Without one there is nowhere to write.
	Configured  bool       `json:"configured"`
	Activation  Activation `json:"activation"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
}

// globalMeta is the fixed description of each file, so the UI does not have to
// carry its own copy of what policy.json is for.
var globalMeta = map[GlobalKind]struct{ Title, Description string }{
	KindPolicy: {
		Title:       "Class of Service and capacity",
		Description: "Per-tenant external dialing, prefix allowlists, spend caps and channel limits. Authorization only: it never says what a name means.",
	},
	KindRoutes: {
		Title:       "DID routing",
		Description: "Which tenant an inbound number belongs to. The tenant's own routing file decides where the call goes from there.",
	},
	KindTrunkPeers: {
		Title:       "SIP trunk peers",
		Description: "Static peers accepted on ingress and used for egress.",
	},
}

// pathForGlobal returns the configured path for a kind, empty when this server
// was not given one.
func (fm *FileManager) pathForGlobal(kind GlobalKind) string {
	switch kind {
	case KindPolicy:
		return fm.policyPath
	case KindRoutes:
		return fm.routesPath
	case KindTrunkPeers:
		return fm.trunkPeersPath
	default:
		return ""
	}
}

// ListGlobalFiles describes every deployment-wide file, configured or not. A
// file this server has no path for is still listed, saying so, rather than
// vanishing — an operator looking for policy.json needs to be told it is not
// wired up here, not shown a shorter list.
func (fm *FileManager) ListGlobalFiles() []GlobalFileInfo {
	out := make([]GlobalFileInfo, 0, len(GlobalKinds))
	for _, kind := range GlobalKinds {
		meta := globalMeta[kind]
		info := GlobalFileInfo{
			Name:        kind,
			Path:        fm.pathForGlobal(kind),
			Configured:  fm.pathForGlobal(kind) != "",
			Activation:  ActivationRestart,
			Title:       meta.Title,
			Description: meta.Description,
		}
		if info.Configured {
			if st, err := os.Stat(info.Path); err == nil {
				info.Exists = true
				info.Size = st.Size()
				info.Modified = st.ModTime()
			}
		}
		out = append(out, info)
	}
	return out
}

// GetGlobalFile reads one deployment-wide file. An absent file reads as empty
// rather than failing, so an editor can create it by saving.
func (fm *FileManager) GetGlobalFile(kind GlobalKind) (string, error) {
	path := fm.pathForGlobal(kind)
	if path == "" {
		return "", fmt.Errorf("%s: %w", kind, ErrNotConfigured)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", kind, err)
	}
	return string(data), nil
}

// PutGlobalFile validates and writes one deployment-wide file.
//
// Same contract as a tenant file: errors refuse the write and nothing touches
// disk; warnings are returned alongside a successful save. What differs is that
// the write does not become effective until the process restarts, which the
// caller has to say out loud.
func (fm *FileManager) PutGlobalFile(kind GlobalKind, content string) (dialplan.Problems, error) {
	path := fm.pathForGlobal(kind)
	if path == "" {
		return nil, fmt.Errorf("%s: %w", kind, ErrNotConfigured)
	}

	problems := fm.validateGlobal(kind, content)
	if problems.HasErrors() {
		return nil, &ValidationError{Tenant: "", File: string(kind), Problems: problems}
	}

	if err := writeFileAtomic(path, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", kind, err)
	}
	return problems, nil
}
