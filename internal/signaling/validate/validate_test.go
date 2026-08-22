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
