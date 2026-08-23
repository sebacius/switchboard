package flowsim

import (
	"context"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// Service adapts the loaded routing configuration to the API's simulator.
//
// It holds the SAME RoutingStore and PolicyConfig the call path uses, read-only.
// That is the point: the operator's question is "what would happen right now",
// and re-reading the files from disk would answer a different one — what would
// happen after a reload nobody has performed.

// TenantSource lists the tenants currently loaded.
type TenantSource interface {
	Tenants() []string
	LoadedAt() time.Time
}

// Service is the API-facing simulator.
type Service struct {
	sources Sources
	tenants TenantSource
}

// NewService builds a simulator over already-loaded configuration.
func NewService(sources Sources, tenants TenantSource) *Service {
	return &Service{sources: sources, tenants: tenants}
}

// Simulate walks one fake call.
func (s *Service) Simulate(ctx context.Context, req Request) (*Result, error) {
	return Run(ctx, s.sources, req)
}

// TenantSummary describes one loaded tenant.
type TenantSummary struct {
	Name     string
	Flows    []string
	Operator string
}

// Loaded lists the tenants in force, with their flow names.
func (s *Service) Loaded() ([]TenantSummary, time.Time) {
	if s.tenants == nil {
		return nil, time.Time{}
	}
	names := s.tenants.Tenants()
	out := make([]TenantSummary, 0, len(names))
	for _, name := range names {
		summary := TenantSummary{Name: name}
		if table, ok := s.sources.Routing.TenantRouting(name); ok && table != nil {
			summary.Operator = table.Operator
		}
		if s.sources.Flows != nil {
			if set, ok := s.sources.Flows.TenantFlows(name); ok && set != nil {
				summary.Flows = set.Names()
			}
		}
		out = append(out, summary)
	}
	return out, s.tenants.LoadedAt()
}

// The live routing store is both sources at once, which is what lets a
// simulation read exactly what the call path reads.
var (
	_ dialplan.RoutingSource = (*dialplan.RoutingStore)(nil)
	_ dialplan.FlowSource    = (*dialplan.RoutingStore)(nil)
	_ TenantSource           = (*dialplan.RoutingStore)(nil)
)
