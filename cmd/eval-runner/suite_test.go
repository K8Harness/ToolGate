package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSuiteLoadsDefaultSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default.yaml")
	writeTestFile(t, path, `
cases:
  - name: small-refund-allow
    input: small-refund
    mustInclude: [refund_small]
    policyOutcome: allow

  - name: large-refund-approval
    input: large-refund
    mustInclude: [refund_large]
    policyOutcome: approvalRequired

  - name: delete-customer-deny
    input: delete-customer
    mustInclude: [delete_record]
    policyOutcome: deny

  - name: slack-pii-redact
    input: slack-pii-message
    mustInclude: [send_slack_message]
    policyOutcome: allow
    mustNotContainInArgs: ["123-45-6789"]
`)

	suite, err := LoadSuite(path)
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	if suite == nil {
		t.Fatal("LoadSuite() suite = nil, want non-nil")
	}
	if len(suite.Cases) != 4 {
		t.Fatalf("len(suite.Cases) = %d, want 4", len(suite.Cases))
	}

	last := suite.Cases[3]
	if last.Name != "slack-pii-redact" {
		t.Fatalf("suite.Cases[3].Name = %q, want slack-pii-redact", last.Name)
	}
	if got := len(last.MustNotContainInArgs); got != 1 || last.MustNotContainInArgs[0] != "123-45-6789" {
		t.Fatalf("suite.Cases[3].MustNotContainInArgs = %v, want [123-45-6789]", last.MustNotContainInArgs)
	}
}

func TestLoadSuiteIgnoresUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unknown-fields.yaml")
	writeTestFile(t, path, `
cases:
  - name: small-refund-allow
    input: small-refund
    mustInclude: [refund_small]
    policyOutcome: allow
    baseline: v2
futureTopLevel: true
`)

	suite, err := LoadSuite(path)
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	if suite == nil {
		t.Fatal("LoadSuite() suite = nil, want non-nil")
	}
	if len(suite.Cases) != 1 {
		t.Fatalf("len(suite.Cases) = %d, want 1", len(suite.Cases))
	}
}

func TestLoadSuiteRejectsInvalidPolicyOutcome(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid-policy.yaml")
	writeTestFile(t, path, `
cases:
  - name: bad-policy
    input: example
    mustInclude: [tool]
    policyOutcome: bad
`)

	suite, err := LoadSuite(path)
	if err == nil {
		t.Fatal("LoadSuite() error = nil, want non-nil")
	}
	if suite != nil {
		t.Fatalf("LoadSuite() suite = %#v, want nil", suite)
	}
	if !strings.Contains(err.Error(), "bad-policy") {
		t.Fatalf("error = %q, want case name", err.Error())
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("error = %q, want invalid value", err.Error())
	}
}

func TestLoadSuiteRequiresPolicyOutcome(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing-policy.yaml")
	writeTestFile(t, path, `
cases:
  - name: missing-policy
    input: example
    mustInclude: [tool]
`)

	suite, err := LoadSuite(path)
	if err == nil {
		t.Fatal("LoadSuite() error = nil, want non-nil")
	}
	if suite != nil {
		t.Fatalf("LoadSuite() suite = %#v, want nil", suite)
	}
	if !strings.Contains(err.Error(), "missing-policy") {
		t.Fatalf("error = %q, want case name", err.Error())
	}
	if !strings.Contains(err.Error(), "policyOutcome") {
		t.Fatalf("error = %q, want field name", err.Error())
	}
}

func TestLoadSuiteAcceptsEmptyCasesList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty-cases.yaml")
	writeTestFile(t, path, "cases: []\n")

	suite, err := LoadSuite(path)
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	if suite == nil {
		t.Fatal("LoadSuite() suite = nil, want non-nil")
	}
	if len(suite.Cases) != 0 {
		t.Fatalf("len(suite.Cases) = %d, want 0", len(suite.Cases))
	}
}

func TestLoadSuiteReturnsNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")

	suite, err := LoadSuite(path)
	if err == nil {
		t.Fatal("LoadSuite() error = nil, want non-nil")
	}
	if suite != nil {
		t.Fatalf("LoadSuite() suite = %#v, want nil", suite)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("errors.Is(err, fs.ErrNotExist) = false, err = %v", err)
	}
}

func TestLoadSuiteParseErrorIncludesPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	writeTestFile(t, path, "cases:\n  - name: broken\n    input: [\n")

	suite, err := LoadSuite(path)
	if err == nil {
		t.Fatal("LoadSuite() error = nil, want non-nil")
	}
	if suite != nil {
		t.Fatalf("LoadSuite() suite = %#v, want nil", suite)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %q, want path %q", err.Error(), path)
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Fatalf("error = %q, want yaml parse context", err.Error())
	}
}

func TestLoadSuiteLoadsRepoDefaultFixtureFromRepoRoot(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}

	repoRoot := filepath.Clean(filepath.Join(cwd, "../.."))
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("os.Chdir(%q) error = %v", repoRoot, err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()

	suite, err := LoadSuite("evalsuite/default.yaml")
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	if suite == nil {
		t.Fatal("LoadSuite() suite = nil, want non-nil")
	}
	if len(suite.Cases) != 4 {
		t.Fatalf("len(suite.Cases) = %d, want 4", len(suite.Cases))
	}
}

func writeTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimLeft(contents, "\n")), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}
