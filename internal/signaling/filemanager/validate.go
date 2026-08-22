package filemanager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// Writes through the config API are validated before they touch disk.
//
// The prompts this surface used to edit were prose: an editor could not do
// better than save what it was given, because "wrong" meant the assistant said
// something unhelpful. A flow graph is different — it can be wrong in ways that
// stop calls being routed at all — and a saved graph that the server would then
// refuse to reload takes the tenant down at the next restart, long after
// whoever saved it has gone home.
//
// So a candidate is checked against the tenant's OTHER file, since the two are
// validated together at load, and nothing is written unless it passes.

// ValidationError reports why a write was refused.
type ValidationError struct {
	Tenant   string
	Problems dialplan.Problems
}

// Error summarises the problems.
func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return fmt.Sprintf("%s: %s", e.Problems[0].Path, e.Problems[0].Message)
	}
	parts := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		parts = append(parts, fmt.Sprintf("%s: %s", p.Path, p.Message))
	}
	return fmt.Sprintf("%d problems: %s", len(parts), strings.Join(parts, "; "))
}

// ValidationProblems exposes the findings so an API can render them one by one
// rather than as a single string.
func (e *ValidationError) ValidationProblems() dialplan.Problems { return e.Problems }

// validateCandidate checks a proposed file against the tenant's existing one.
func (fm *FileManager) validateCandidate(tenant string, kind FileKind, content string) dialplan.Problems {
	problem := func(path, msg string) dialplan.Problems {
		return dialplan.Problems{{
			Tenant: tenant, Path: path, Message: msg, Severity: dialplan.SeverityError,
		}}
	}

	switch kind {
	case KindRouting:
		var table dialplan.RoutingTable
		if err := json.Unmarshal([]byte(content), &table); err != nil {
			return problem("routing", "not valid JSON: "+err.Error())
		}
		// The table's own checks — patterns, group references, retired
		// vocabulary — run through the loader's validator, not a copy of it.
		if err := dialplan.ValidateTable(tenant, &table, []byte(content)); err != nil {
			return problem("routing", err.Error())
		}
		// A routing change can orphan a group a flow dials, so the flows are
		// re-checked against the candidate table.
		set := fm.existingFlows(tenant)
		return dialplan.CheckFlows(tenant, &table, set)

	case KindFlows:
		if strings.TrimSpace(content) == "" {
			// Saving an empty flow file removes the graphs, which is allowed.
			return nil
		}
		var set dialplan.FlowSet
		if err := json.Unmarshal([]byte(content), &set); err != nil {
			return problem("flows", "not valid JSON: "+err.Error())
		}
		table, err := fm.existingTable(tenant)
		if err != nil {
			return problem("flows", err.Error())
		}
		return dialplan.CheckFlows(tenant, table, &set)

	default:
		return problem(string(kind), "unknown file kind")
	}
}

// existingTable loads the tenant's current routing table, which a candidate
// flow file is checked against.
func (fm *FileManager) existingTable(tenant string) (*dialplan.RoutingTable, error) {
	data, err := os.ReadFile(filepath.Join(fm.tenantsDir, tenant+routingSuffix))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"tenant %q has no routing table; flows need one to name their operator and ring groups", tenant)
		}
		return nil, fmt.Errorf("read routing table: %w", err)
	}
	var table dialplan.RoutingTable
	if err := json.Unmarshal(data, &table); err != nil {
		return nil, fmt.Errorf("the tenant's existing routing table is not valid JSON: %w", err)
	}
	return &table, nil
}

// existingFlows loads the tenant's current flows, or nil when it has none.
func (fm *FileManager) existingFlows(tenant string) *dialplan.FlowSet {
	data, err := os.ReadFile(filepath.Join(fm.tenantsDir, tenant+flowsSuffix))
	if err != nil {
		return nil
	}
	var set dialplan.FlowSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil
	}
	return &set
}
