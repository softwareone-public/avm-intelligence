package iaclens

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizePreservesOwnershipAndStatuses(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
	  "iaclens": {
	    "schema_version": 2,
	    "source_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	    "repository": {"root": "/private/worktree", "name": "source"},
	    "ruleset": {"name": "avm-intelligence-offline", "version": 1, "digest": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	    "units": [
	      {"path": ".", "symbols": [{"kind": "variable", "file": "main.tf", "line": 1, "attributes": {"description": "secret-looking expression"}}]},
	      {"path": "examples/default", "symbols": []}
	    ],
	    "analysis": {"units": [
	      {"path": ".", "role": "component", "classification": {"kind": "module", "method": "override", "reason": "factory root override"}, "checks": [
	        {"id": "input-descriptions", "engine": "go", "status": "fail", "severity": "warning", "message": "missing description", "evidence": ["main.tf:1"]},
	        {"id": "module-purpose", "engine": "jev", "status": "unchecked", "severity": "info", "message": "model disabled"}
	      ]},
	      {"path": "examples/default", "role": "example", "owner": ".", "owner_scope": "component", "classification": {"kind": "deployment", "method": "go", "reason": "declares-backend"}, "checks": [
	        {"id": "deployment-core-version", "engine": "go", "status": "not_applicable", "severity": "warning", "message": "no terraform block"}
	      ]}
	    ]}
	  }
	}`)
	report, err := Normalize(raw, Provenance{ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("c", 40), CLIVersion: "0.1.0", CLICommit: strings.Repeat("d", 40), AdapterVersion: AdapterVersion})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(report.Units) != 2 || report.Units[1].Owner != "." || report.Units[1].OwnerScope != "component" {
		t.Fatalf("ownership was flattened: %#v", report.Units)
	}
	if report.Units[0].Checks[0].Status != "fail" || report.Units[0].Checks[1].Status != "unchecked" || report.Units[1].Checks[0].Status != "not_applicable" {
		t.Fatalf("statuses changed: %#v", report.Units)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-looking") || strings.Contains(string(encoded), "/private/worktree") {
		t.Fatal("public projection retained source expression or local root")
	}
}

func TestNormalizeRejectsUnknownSchemaAndStatus(t *testing.T) {
	t.Parallel()
	provenance := Provenance{ModuleID: "demo", SourceSHA: strings.Repeat("a", 40), CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40), AdapterVersion: AdapterVersion}
	if _, err := Normalize([]byte(`{"iaclens":{"schema_version":3}}`), provenance); err == nil {
		t.Fatal("future schema was accepted")
	}
	raw := []byte(`{"iaclens":{"schema_version":2,"source_digest":"aa","ruleset":{"name":"x","version":1,"digest":"bb"},"units":[{"path":".","symbols":[]}],"analysis":{"units":[{"path":".","role":"component","classification":{"kind":"module","method":"go","reason":"fixture"},"checks":[{"id":"x","engine":"go","status":"passed","severity":"warning","message":"bad"}]}]}}}`)
	if _, err := Normalize(raw, provenance); err == nil {
		t.Fatal("unknown status was accepted")
	}
}
