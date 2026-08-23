package filemanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validRouting = `{"operator":"user/100","extensions":{"100":"flow/main"},
	"groups":{"claims":{"strategy":"sequential","members":["user/130"]}}}`

const validFlows = `{"flows":{"main":{
	"start":"ring",
	"nodes":{
		"ring":{"type":"dial_user","entry":{"target":"group/claims"},
			"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
		"bye":{"type":"hangup","entry":{}}
	}}}}`

func newManager(t *testing.T) (*FileManager, string) {
	t.Helper()
	dir := t.TempDir()
	return New(Config{TenantsDir: dir}), dir
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// A tenant with only a routing table is a normal tenant: flows are optional.
func TestListTenantsReportsWhichHaveFlows(t *testing.T) {
	fm, dir := newManager(t)
	writeFile(t, dir, "simple.routing.json", validRouting)
	writeFile(t, dir, "full.routing.json", validRouting)
	writeFile(t, dir, "full.flows.json", validFlows)

	tenants, err := fm.ListTenants()
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("expected 2 tenants, got %d: %+v", len(tenants), tenants)
	}

	byName := map[string]TenantInfo{}
	for _, tn := range tenants {
		byName[tn.Name] = tn
	}
	if byName["simple"].HasFlows {
		t.Error("a tenant with no flow file must not report having flows")
	}
	if !byName["full"].HasFlows {
		t.Error("a tenant with a flow file should report having them")
	}
}

// The point of validating writes: a graph that would not load must not reach
// disk, or the tenant loses its routing at the next restart.
func TestInvalidFlowIsRejectedAndNothingIsWritten(t *testing.T) {
	fm, dir := newManager(t)
	writeFile(t, dir, "acme.routing.json", validRouting)
	writeFile(t, dir, "acme.flows.json", validFlows)

	broken := `{"flows":{"main":{
		"start":"a",
		"nodes":{
			"a":{"type":"tts","entry":{"text":"one"},"exits":{"done":"b"}},
			"b":{"type":"tts","entry":{"text":"two"},"exits":{"done":"a"}}
		}}}}`

	_, err := fm.PutTenantFile("acme", KindFlows, broken)
	if err == nil {
		t.Fatal("a cyclic flow must be refused")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("the error should say what is wrong: %v", err)
	}

	// Nothing changed on disk.
	current, _ := fm.GetTenantFile("acme", KindFlows)
	if !strings.Contains(current, "group/claims") {
		t.Error("the previous flow file must survive a rejected write")
	}
}

// The refusal carries structured problems so a UI can point at the node.
func TestRejectionCarriesProblems(t *testing.T) {
	fm, dir := newManager(t)
	writeFile(t, dir, "acme.routing.json", validRouting)

	_, err := fm.PutTenantFile("acme", KindFlows, `{"flows":{"main":{
		"start":"d",
		"nodes":{"d":{"type":"dial_user","entry":{"target":"group/ghost"},
			"exits":{"no_answer":"d2","busy":"d2","rejected":"d2","unavailable":"d2"}},
			"d2":{"type":"hangup","entry":{}}}}}}`)

	verr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected a ValidationError, got %T: %v", err, err)
	}
	if len(verr.ValidationProblems()) == 0 {
		t.Fatal("the error should carry the problems")
	}
	if !strings.Contains(verr.ValidationProblems()[0].Path, "nodes.d") {
		t.Errorf("the problem should name the node: %+v", verr.ValidationProblems()[0])
	}
}

// A routing change that orphans a group a flow dials must be refused too — the
// two files are validated together at load, so they must be together on write.
func TestRoutingChangeThatBreaksAFlowIsRejected(t *testing.T) {
	fm, dir := newManager(t)
	writeFile(t, dir, "acme.routing.json", validRouting)
	writeFile(t, dir, "acme.flows.json", validFlows)

	// Removing the claims group leaves the flow dialing nothing.
	_, err := fm.PutTenantFile("acme", KindRouting,
		`{"operator":"user/100","extensions":{"100":"flow/main"},"groups":{}}`)

	if err == nil {
		t.Fatal("removing a group a flow dials must be refused")
	}
	if !strings.Contains(err.Error(), "claims") {
		t.Errorf("the error should name the group the flow needs: %v", err)
	}
}

// A valid write goes through.
func TestValidWriteSucceeds(t *testing.T) {
	fm, dir := newManager(t)
	writeFile(t, dir, "acme.routing.json", validRouting)

	if _, err := fm.PutTenantFile("acme", KindFlows, validFlows); err != nil {
		t.Fatalf("a valid flow should be accepted: %v", err)
	}
	got, err := fm.GetTenantFile("acme", KindFlows)
	if err != nil {
		t.Fatalf("GetTenantFile: %v", err)
	}
	if !strings.Contains(got, "group/claims") {
		t.Error("the flow should have been written")
	}
}

// Deleting a tenant must not leave an orphaned flow file, which would fail the
// next load.
func TestDeleteRemovesBothFiles(t *testing.T) {
	fm, dir := newManager(t)
	writeFile(t, dir, "acme.routing.json", validRouting)
	writeFile(t, dir, "acme.flows.json", validFlows)

	if err := fm.DeleteTenant("acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	for _, name := range []string{"acme.routing.json", "acme.flows.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", name)
		}
	}
}

// Path traversal through a tenant name must be impossible.
func TestUnsafeTenantNameIsRejected(t *testing.T) {
	fm, _ := newManager(t)
	for _, name := range []string{"../escape", "/etc/passwd", ""} {
		if _, err := fm.GetTenantFile(name, KindRouting); err == nil {
			t.Errorf("tenant name %q should be rejected", name)
		}
	}
}
