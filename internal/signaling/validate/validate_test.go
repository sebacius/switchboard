package validate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig lays out a tenant directory.
func writeConfig(t *testing.T, routing, flows string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "acme.routing.json"), []byte(routing), 0o600); err != nil {
		t.Fatalf("write routing: %v", err)
	}
	if flows != "" {
		if err := os.WriteFile(filepath.Join(dir, "acme.flows.json"), []byte(flows), 0o600); err != nil {
			t.Fatalf("write flows: %v", err)
		}
	}
	return dir
}

// writeRoutes lays out a DID -> tenant table.
func writeRoutes(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "routes.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	return path
}

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return path
}

func TestValidConfigurationExitsZero(t *testing.T) {
	dir := writeConfig(t,
		`{"operator":"user/100","extensions":{"100":"flow/main"}}`,
		`{"flows":{"main":{"start":"bye","nodes":{"bye":{"type":"hangup","entry":{}}}}}}`)
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)

	var out bytes.Buffer
	if code := Run([]string{"--routing-path", dir, "--policy-config", policy}, &out); code != ExitOK {
		t.Fatalf("exit = %d, want %d. output:\n%s", code, ExitOK, out.String())
	}
}

// An operator fixing a graph wants every problem at once, not the first.
func TestEveryProblemIsReported(t *testing.T) {
	dir := writeConfig(t,
		`{"operator":"user/100","extensions":{"100":"flow/main"}}`,
		`{"flows":{"main":{
			"start":"a",
			"nodes":{
				"a":{"type":"tts","entry":{"text":"one"},"exits":{"done":"b"}},
				"b":{"type":"tts","entry":{"text":"two"},"exits":{"done":"a"}},
				"orphan":{"type":"hangup","entry":{}}
			}}}}`)
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)

	var out bytes.Buffer
	code := Run([]string{"--routing-path", dir, "--policy-config", policy}, &out)

	if code != ExitProblem {
		t.Fatalf("exit = %d, want %d", code, ExitProblem)
	}
	text := out.String()
	// The cycle path is named, not merely its existence.
	if !strings.Contains(text, "a -> b -> a") {
		t.Errorf("the cycle path should be named:\n%s", text)
	}
	if !strings.Contains(text, "orphan") {
		t.Errorf("the unreachable node should be reported too:\n%s", text)
	}
	if !strings.Contains(text, "2 error(s)") {
		t.Errorf("both problems should be counted:\n%s", text)
	}
}

// Class of Service is checked at load, so a flow that could never place its
// call is caught before a caller finds out.
func TestClassOfServiceIsChecked(t *testing.T) {
	dir := writeConfig(t,
		`{"operator":"user/100","extensions":{"100":"flow/main"},
		  "symbolic_targets":{"afterhours":"+19005551212"}}`,
		`{"flows":{"main":{
			"start":"out",
			"nodes":{
				"out":{"type":"dial_external","entry":{"target":"afterhours"},
					"exits":{"no_answer":"bye","busy":"bye","denied":"bye","failed":"bye"}},
				"bye":{"type":"hangup","entry":{}}
			}}}}`)
	// External dialling disabled: the shipped default posture.
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)

	var out bytes.Buffer
	code := Run([]string{"--routing-path", dir, "--policy-config", policy}, &out)

	if code != ExitProblem {
		t.Fatalf("exit = %d, want a problem. output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "class of service") {
		t.Errorf("the error should say the destination is not permitted:\n%s", out.String())
	}
}

// A missing directory is an error, not a silent pass.
func TestMissingDirectoryIsAnError(t *testing.T) {
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)
	var out bytes.Buffer

	if code := Run([]string{"--routing-path", "/nonexistent", "--policy-config", policy}, &out); code != ExitProblem {
		t.Fatalf("exit = %d, want %d", code, ExitProblem)
	}
}

// The bug that shipped: routes.json pointed at a tenant nobody had configured,
// and the only sign was a 404 on a real call.
func TestDIDRoutingToAMissingTenantIsAnError(t *testing.T) {
	dir := writeConfig(t,
		`{"operator":"user/100","extensions":{"100":"user/100"}}`, "")
	routes := writeRoutes(t, dir, `{"dids":{"+15551234567":"ghost"}}`)
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)

	var out bytes.Buffer
	code := Run([]string{
		"--routing-path", dir, "--policy-config", policy, "--routes-path", routes,
	}, &out)

	if code != ExitProblem {
		t.Fatalf("exit = %d, want %d. output:\n%s", code, ExitProblem, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "ghost") {
		t.Errorf("the error should name the missing tenant:\n%s", text)
	}
	if !strings.Contains(text, "404") {
		t.Errorf("the error should say what a caller would experience:\n%s", text)
	}
}

// The likeliest operator mistake: adding a DID to the tenant file and
// forgetting the global one. A warning, because the configuration still loads.
func TestTenantDIDWithNoRouteIsWarned(t *testing.T) {
	dir := writeConfig(t,
		`{"operator":"user/100","extensions":{"100":"user/100"},
		  "dids":{"+15558001200":"user/100"}}`, "")
	routes := writeRoutes(t, dir, `{"dids":{}}`)
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)

	var out bytes.Buffer
	code := Run([]string{
		"--routing-path", dir, "--policy-config", policy, "--routes-path", routes,
	}, &out)

	if code != ExitOK {
		t.Fatalf("a missing route is a warning, not an error: exit %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "+15558001200") {
		t.Errorf("the warning should name the orphaned DID:\n%s", out.String())
	}
}

// A DID the global table does route is not warned about.
func TestRoutedTenantDIDIsNotWarned(t *testing.T) {
	dir := writeConfig(t,
		`{"operator":"user/100","extensions":{"100":"user/100"},
		  "dids":{"+15558001200":"user/100"}}`, "")
	routes := writeRoutes(t, dir, `{"dids":{"+15558001200":"acme"}}`)
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)

	var out bytes.Buffer
	if code := Run([]string{
		"--routing-path", dir, "--policy-config", policy, "--routes-path", routes,
	}, &out); code != ExitOK {
		t.Fatalf("exit = %d, want clean:\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "+15558001200") {
		t.Errorf("a correctly routed DID should not be mentioned:\n%s", out.String())
	}
}

// The cross-check uses the same matcher the call path does, so a DID written
// one way and routed the other still counts as covered.
func TestCrossCheckToleratesTheLeadingPlus(t *testing.T) {
	dir := writeConfig(t,
		`{"operator":"user/100","extensions":{"100":"user/100"},
		  "dids":{"+15558001200":"user/100"}}`, "")
	// Routed WITHOUT the plus; the tenant handles it WITH one.
	routes := writeRoutes(t, dir, `{"dids":{"15558001200":"acme"}}`)
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)

	var out bytes.Buffer
	Run([]string{"--routing-path", dir, "--policy-config", policy, "--routes-path", routes}, &out)

	if strings.Contains(out.String(), "+15558001200") {
		t.Errorf("the two E.164 forms are the same number and must not warn:\n%s", out.String())
	}
}

// A tenant using patterns for its DIDs is not cross-checked rather than checked
// badly: deciding whether one pattern contains another is a harder problem than
// this warning is worth.
func TestPatternDIDsAreNotCrossChecked(t *testing.T) {
	dir := writeConfig(t,
		`{"operator":"user/100","extensions":{"100":"user/100"},
		  "dids":{"+1555800XXXX":"user/100"}}`, "")
	routes := writeRoutes(t, dir, `{"dids":{}}`)
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)

	var out bytes.Buffer
	Run([]string{"--routing-path", dir, "--policy-config", policy, "--routes-path", routes}, &out)

	if strings.Contains(out.String(), "XXXX") {
		t.Errorf("pattern DIDs should not be warned about:\n%s", out.String())
	}
}

// A missing routes.json is the safe posture — no inbound routing — not a
// failure, so a signaling-only deployment still validates.
func TestMissingRoutesFileIsNotAnError(t *testing.T) {
	dir := writeConfig(t, `{"operator":"user/100","extensions":{"100":"user/100"}}`, "")
	policy := writePolicy(t, `{"default_channel_limit":10,"tenants":{}}`)

	var out bytes.Buffer
	if code := Run([]string{
		"--routing-path", dir, "--policy-config", policy,
		"--routes-path", filepath.Join(dir, "absent.json"),
	}, &out); code != ExitOK {
		t.Fatalf("a missing routes file must not fail validation: exit %d\n%s", code, out.String())
	}
}
