package filemanager

import "time"

// Status answers one question: is what is on disk what is serving calls?
//
// An editor that only remembers "I saved something" gets this wrong in every
// interesting case — a second operator in another browser, a UI restart, a
// signaling restart that already picked the change up. Comparing file mtimes
// against the load stamp is the same answer for everyone who asks.
type Status struct {
	// LoadedAt is when the routing cache now in force was built.
	LoadedAt time.Time `json:"loaded_at"`
	// StartedAt is when this file manager — and so the process — came up.
	StartedAt time.Time `json:"started_at"`
	// StaleTenants have been written since LoadedAt. A reload activates them.
	StaleTenants []string `json:"stale_tenants"`
	// StaleGlobals have been written since StartedAt. Only a restart activates
	// them, which is why they are a separate list rather than a longer one.
	StaleGlobals []GlobalKind `json:"stale_globals"`
}

// loadStamper is implemented by a reloader that can say when its cache was
// built. The routing store does; a reloader that does not simply reports a zero
// time, and nothing is ever called stale on the strength of it.
type loadStamper interface {
	LoadedAt() time.Time
}

// ConfigStatus compares the files on disk against what has been loaded.
func (fm *FileManager) ConfigStatus() Status {
	st := Status{StartedAt: fm.startedAt}

	if stamper, ok := fm.settingsReloader.(loadStamper); ok {
		st.LoadedAt = stamper.LoadedAt()
	}

	if !st.LoadedAt.IsZero() {
		if tenants, err := fm.ListTenants(); err == nil {
			for _, t := range tenants {
				if t.Modified.After(st.LoadedAt) {
					st.StaleTenants = append(st.StaleTenants, t.Name)
				}
			}
		}
	}

	for _, info := range fm.ListGlobalFiles() {
		if !info.Exists {
			continue
		}
		if info.Modified.After(st.StartedAt) {
			st.StaleGlobals = append(st.StaleGlobals, info.Name)
		}
	}

	return st
}

// ReloadResult says what a reload actually activated. Half of the config API's
// files cannot be reloaded at all, so a bare "ok" would let an operator believe
// a policy edit is live.
type ReloadResult struct {
	Reloaded    []string     `json:"reloaded"`
	NotReloaded []GlobalKind `json:"not_reloaded"`
	Message     string       `json:"message"`
}

// ReloadDetail reloads and reports what that did and did not cover.
func (fm *FileManager) ReloadDetail() (ReloadResult, error) {
	if err := fm.Reload(); err != nil {
		return ReloadResult{}, err
	}
	res := ReloadResult{
		Reloaded: []string{"tenant routing", "tenant flows"},
		Message:  "Tenant files are live.",
	}
	for _, info := range fm.ListGlobalFiles() {
		if info.Configured {
			res.NotReloaded = append(res.NotReloaded, info.Name)
		}
	}
	if len(res.NotReloaded) > 0 {
		res.Message += " The deployment-wide files only take effect after the signaling server restarts."
	}
	return res, nil
}
