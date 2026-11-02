package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	types "github.com/sebas/switchboard/api/types/v1"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/filemanager"
)

// api/types/v1 is the declared contract between this server and the UI, and for
// most of its life no handler here imported it — the tenant listing built a
// map[string]interface{} by hand. That makes the mirror decorative: fields can
// be added to it that the server never emits, and nothing fails.
//
// These tests make the wire shape the thing under test, so a divergence is a
// failure rather than a surprise in the browser.

type fakeFiles struct {
	tenants  []filemanager.TenantInfo
	detailed []filemanager.TenantInfo
	// detailCalls counts the expensive listing, so a test can assert the cheap
	// path was not silently upgraded.
	detailCalls int
}

func (f *fakeFiles) ListTenants() ([]filemanager.TenantInfo, error) { return f.tenants, nil }
func (f *fakeFiles) ListTenantsDetailed() ([]filemanager.TenantInfo, error) {
	f.detailCalls++
	return f.detailed, nil
}
func (f *fakeFiles) GetTenant(string) (string, error) { return "", nil }
func (f *fakeFiles) CreateTenant(string, string) (dialplan.Problems, error) {
	return nil, nil
}
func (f *fakeFiles) PutTenant(string, string) (dialplan.Problems, error) { return nil, nil }
func (f *fakeFiles) DeleteTenant(string) error                           { return nil }
func (f *fakeFiles) GetTenantFile(string, filemanager.FileKind) (string, error) {
	return "", nil
}
func (f *fakeFiles) PutTenantFile(string, filemanager.FileKind, string) (dialplan.Problems, error) {
	return nil, nil
}
func (f *fakeFiles) ListGlobalFiles() []filemanager.GlobalFileInfo { return nil }
func (f *fakeFiles) GetGlobalFile(filemanager.GlobalKind) (string, error) {
	return "", nil
}
func (f *fakeFiles) PutGlobalFile(filemanager.GlobalKind, string) (dialplan.Problems, error) {
	return nil, nil
}
func (f *fakeFiles) ConfigStatus() filemanager.Status { return filemanager.Status{} }
func (f *fakeFiles) Reload() error                    { return nil }
func (f *fakeFiles) ReloadDetail() (filemanager.ReloadResult, error) {
	return filemanager.ReloadResult{}, nil
}

func listTenants(t *testing.T, files *fakeFiles, query string) []types.TenantFile {
	t.Helper()
	s := &Server{fileProvider: files}

	rec := httptest.NewRecorder()
	s.handleConfigTenantList(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config/tenants"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	// Decoding into the SHARED type is the assertion: if the handler emits a
	// shape this cannot represent, the test is what notices.
	var got []types.TenantFile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the listing does not decode into the shared type: %v\n%s", err, rec.Body.String())
	}
	return got
}

func TestTheCheapListingSaysWhatItAlwaysSaid(t *testing.T) {
	files := &fakeFiles{tenants: []filemanager.TenantInfo{
		{Name: "devtenant", Size: 3117, Modified: time.Unix(0, 0).UTC(), HasFlows: true},
		{Name: "routing-only", Size: 400, Modified: time.Unix(0, 0).UTC()},
	}}

	got := listTenants(t, files, "")
	if len(got) != 2 {
		t.Fatalf("listed %d tenants, want 2", len(got))
	}
	if got[0].Name != "devtenant" || !got[0].HasFlows {
		t.Errorf("first entry = %+v", got[0])
	}
	if got[1].HasFlows {
		t.Error("a routing-only tenant must not report flows")
	}
	if files.detailCalls != 0 {
		t.Error("the cheap listing did the expensive work")
	}
}

// The expensive listing has to be asked for. An endpoint that sometimes does
// forty times the work is one that eventually gets polled by accident.
func TestTheDetailedListingIsOptIn(t *testing.T) {
	files := &fakeFiles{
		tenants: []filemanager.TenantInfo{{Name: "devtenant"}},
		detailed: []filemanager.TenantInfo{{
			Name: "devtenant", HasFlows: true,
			ExtensionsCount: 7, DIDsCount: 2, GroupsCount: 1,
			FlowNames: []string{"main-ivr"}, HasWarnings: true,
			SearchTerms: []string{"+15558001200", "700", "engineering", "main-ivr"},
		}},
	}

	plain := listTenants(t, files, "")
	if plain[0].ExtensionsCount != 0 || len(plain[0].FlowNames) != 0 {
		t.Errorf("the cheap listing reported contents it did not compute: %+v", plain[0])
	}

	detailed := listTenants(t, files, "?detail=1")
	if files.detailCalls != 1 {
		t.Errorf("detailed listing called %d times, want once", files.detailCalls)
	}
	got := detailed[0]
	if got.ExtensionsCount != 7 || got.DIDsCount != 2 || got.GroupsCount != 1 {
		t.Errorf("counts = %d/%d/%d, want 7/2/1", got.ExtensionsCount, got.DIDsCount, got.GroupsCount)
	}
	if len(got.FlowNames) != 1 || got.FlowNames[0] != "main-ivr" {
		t.Errorf("flow names = %v", got.FlowNames)
	}
	if !got.HasWarnings {
		t.Error("the warning state did not survive the wire")
	}
	// The search terms are what make "1200" find the tenant owning
	// +15558001200 from one request.
	if len(got.SearchTerms) != 4 {
		t.Errorf("search terms = %v, want the four that were listed", got.SearchTerms)
	}
}

// A tenant with nothing must report zero rather than omitting the key, or a
// client cannot tell "none" from "not asked for". FlowNames must be [] and not
// null for the same reason — a search that ranges over it would break on null.
func TestAnEmptyTenantReportsZeroRatherThanNothing(t *testing.T) {
	files := &fakeFiles{detailed: []filemanager.TenantInfo{{Name: "bare"}}}

	rec := httptest.NewRecorder()
	s := &Server{fileProvider: files}
	s.handleConfigTenantList(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config/tenants?detail=1", nil))

	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"extensions_count", "dids_count", "groups_count", "flow_names", "search_terms", "has_warnings"} {
		if _, present := raw[0][key]; !present {
			t.Errorf("%q is missing from the response; a client cannot tell none from not-asked-for", key)
		}
	}
	for _, key := range []string{"flow_names", "search_terms"} {
		if raw[0][key] == nil {
			t.Errorf("%s is null, which a client ranging over it cannot handle", key)
		}
	}
}

// The listing must be usable without a file manager configured.
func TestTheListingSaysSoWhenFileManagementIsOff(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleConfigTenantList(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config/tenants", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
