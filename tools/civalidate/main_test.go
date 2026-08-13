package main

import (
	"strings"
	"testing"
)

const validStandards = `
schema_version: 1
aggregates:
  - workflow: CI
    job: ci-required
standards:
  - id: generated-contract-drift
    rationale: Generated artifacts must match their declared sources.
    owner: merge-required-ci
    fallback_owner: local-pre-push
    witnesses:
      - id: generated-drift-current
        status: required
        command: go run ./tools/skillgen/
        triggers: [pull-request, main-push]
        platforms: [linux]
        job: {workflow: CI, id: skill-drift, aggregate: true}
        purpose:
          met: [candidate-property, ci-best-environment, contributor-actionable]
          evidence: Candidate-generated output is compared with the repository.
        authority:
          met: [deterministic, hermetic-enough, anti-vacuous, actionable, bounded, reproducible-or-diagnosable, owned]
          evidence: A stale generated fixture turns the witness red.
        anti_vacuity: The generated-path inventory is nonempty and a stale fixture fails.
        runtime: {bound_seconds: 300, recent: null}
        coverage:
          packages: []
          package_classes: []
          package_roles: []
          platform_contracts: []
          paths: [tools/skillgen/]
          generated_paths: [skills/, internal/web/dist/]
        diagnostics:
          local_command: go run ./tools/skillgen/
          local_make_target: null
          hosted_artifact: null
          failure_artifact: generated-diff
          formats: [diff]
        qualification:
          state: required-requalifying
          history:
            - date: "2026-08-12"
              state: required-requalifying
              reason: Imported from the existing required workflow.
`

const validOwnership = `
schema_version: 1
module: github.com/xoai/sage-wiki
packages:
  - path: .
    class: test-owned
    owner: generated-drift-current
    roles: []
`

const validPlatforms = `
schema_version: 1
focused_goos: [windows, darwin]
contracts:
  - id: manifest-lock-windows
    witness: generated-drift-current
    package: internal/manifest
    goos: windows
    goarch: any
    invariant: Concurrent manifest mutations preserve every update under Windows file sharing.
    tests: [TestConcurrentManifestUpdates]
  - id: engine-flock-darwin
    witness: generated-drift-current
    package: pkg/engine
    goos: darwin
    goarch: any
    invariant: A nonblocking flock permits exactly one workspace writer.
    tests: [TestWorkspaceLock]
inventory:
  - path: internal/manifest/lock.go
    signals:
      - kind: runtime-goos
        goos: [windows]
        goarch: []
    contracts: [manifest-lock-windows]
    reason: The generic lock implementation classifies Windows sharing errors.
`

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		parse func(string, []byte) error
	}{
		{
			name: "empty standards",
			data: " \n\t",
			parse: func(name string, data []byte) error {
				_, err := parseStandards(name, data)
				return err
			},
		},
		{
			name: "unknown standards field",
			data: validStandards + "unknown_field: true\n",
			parse: func(name string, data []byte) error {
				_, err := parseStandards(name, data)
				return err
			},
		},
		{
			name: "duplicate mapping key",
			data: strings.Replace(validOwnership, "schema_version: 1", "schema_version: 1\nschema_version: 1", 1),
			parse: func(name string, data []byte) error {
				_, err := parsePackageOwnership(name, data)
				return err
			},
		},
		{
			name: "multiple platform documents",
			data: validPlatforms + "---\n" + validPlatforms,
			parse: func(name string, data []byte) error {
				_, err := parsePlatformContracts(name, data)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.parse(tt.name+".yaml", []byte(tt.data)); err == nil {
				t.Fatal("parse succeeded, want error")
			}
		})
	}
}

func TestStandards(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
		code ProblemCode
	}{
		{
			name: "missing rationale",
			edit: func(s string) string {
				return strings.Replace(s, "    rationale: Generated artifacts must match their declared sources.\n", "", 1)
			},
			code: ProblemMissingField,
		},
		{
			name: "unknown owner",
			edit: func(s string) string { return strings.Replace(s, "owner: merge-required-ci", "owner: nobody", 1) },
			code: ProblemUnknownEnum,
		},
		{
			name: "unknown status",
			edit: func(s string) string { return strings.Replace(s, "status: required", "status: trusted", 1) },
			code: ProblemUnknownEnum,
		},
		{
			name: "duplicate standard id",
			edit: func(s string) string {
				return s + `
  - id: generated-contract-drift
    rationale: Duplicate standard fixture.
    owner: merge-required-ci
    fallback_owner: local-pre-push
    witnesses: []
`
			},
			code: ProblemDuplicateID,
		},
		{
			name: "duplicate witness id",
			edit: func(s string) string {
				start := strings.Index(s, "      - id: generated-drift-current")
				return s + s[start:]
			},
			code: ProblemDuplicateID,
		},
		{
			name: "empty generated inventory",
			edit: func(s string) string {
				return strings.Replace(s, "generated_paths: [skills/, internal/web/dist/]", "generated_paths: []", 1)
			},
			code: ProblemGeneratedInventoryEmpty,
		},
		{
			name: "required witness has no reproduction path",
			edit: func(s string) string {
				return strings.Replace(s, "local_command: go run ./tools/skillgen/", "local_command: null", 1)
			},
			code: ProblemMissingDiagnostic,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			standards, err := parseStandards("standards.yaml", []byte(tt.edit(validStandards)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			assertProblemCode(t, validateStatic(Manifests{Standards: standards}), tt.code)
		})
	}
}

func TestOwnership(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
		code ProblemCode
	}{
		{
			name: "missing package class",
			edit: func(s string) string { return strings.Replace(s, "    class: test-owned\n", "", 1) },
			code: ProblemMissingField,
		},
		{
			name: "unknown package class",
			edit: func(s string) string { return strings.Replace(s, "class: test-owned", "class: sometimes-tested", 1) },
			code: ProblemUnknownEnum,
		},
		{
			name: "duplicate package",
			edit: func(s string) string {
				return s + "  - path: .\n    class: test-owned\n    owner: generated-drift-current\n    roles: []\n"
			},
			code: ProblemDuplicateID,
		},
		{
			name: "dangling owner",
			edit: func(s string) string {
				return strings.Replace(s, "owner: generated-drift-current", "owner: missing-witness", 1)
			},
			code: ProblemDanglingReference,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			standards, err := parseStandards("standards.yaml", []byte(validStandards))
			if err != nil {
				t.Fatal(err)
			}
			packages, err := parsePackageOwnership("package-ownership.yaml", []byte(tt.edit(validOwnership)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			assertProblemCode(t, validateStatic(Manifests{Standards: standards, Packages: packages}), tt.code)
		})
	}
}

func TestPlatform(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
		code ProblemCode
	}{
		{
			name: "duplicate platform contract id",
			edit: func(s string) string {
				return strings.Replace(s, "inventory:\n", `  - id: manifest-lock-windows
    witness: generated-drift-current
    package: internal/manifest
    goos: windows
    goarch: any
    invariant: Duplicate platform contract fixture.
    tests: [TestConcurrentManifestUpdates]
inventory:
`, 1)
			},
			code: ProblemDuplicateID,
		},
		{
			name: "empty platform inventory",
			edit: func(s string) string { return s[:strings.Index(s, "inventory:")] + "inventory: []\n" },
			code: ProblemPlatformInventoryEmpty,
		},
		{
			name: "empty focused contracts",
			edit: func(s string) string {
				return strings.Replace(s, "focused_goos: [windows, darwin]", "focused_goos: []", 1)
			},
			code: ProblemPlatformContractEmpty,
		},
		{
			name: "unknown signal",
			edit: func(s string) string { return strings.Replace(s, "kind: runtime-goos", "kind: intuition", 1) },
			code: ProblemUnknownEnum,
		},
		{
			name: "dangling inventory contract",
			edit: func(s string) string {
				return strings.Replace(s, "contracts: [manifest-lock-windows]", "contracts: [missing-contract]", 1)
			},
			code: ProblemDanglingReference,
		},
		{
			name: "contract without tests",
			edit: func(s string) string {
				return strings.Replace(s, "tests: [TestConcurrentManifestUpdates]", "tests: []", 1)
			},
			code: ProblemPlatformContractEmpty,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			standards, err := parseStandards("standards.yaml", []byte(validStandards))
			if err != nil {
				t.Fatal(err)
			}
			platforms, err := parsePlatformContracts("platform-contracts.yaml", []byte(tt.edit(validPlatforms)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			assertProblemCode(t, validateStatic(Manifests{Standards: standards, Platforms: platforms}), tt.code)
		})
	}
}

func assertProblemCode(t *testing.T, problems Problems, want ProblemCode) {
	t.Helper()
	for _, problem := range problems {
		if problem.Code == want {
			return
		}
	}
	t.Fatalf("problems = %#v, want code %q", problems, want)
}
