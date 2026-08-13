package main

import (
	"strings"
	"testing"
	"testing/fstest"
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
  - path: internal/manifest
    class: test-owned
    owner: generated-drift-current
    roles: []
  - path: pkg/engine
    class: test-owned
    owner: generated-drift-current
    roles: []
  - path: tools/skillgen
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
		{
			name: "flow mapping prose with an unquoted colon",
			data: strings.Replace(validPlatforms,
				"reason: The generic lock implementation classifies Windows sharing errors.",
				"reason: Native separators: Windows sharing errors are classified.", 1),
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
			name: "unknown signal GOOS",
			edit: func(s string) string { return strings.Replace(s, "goos: [windows]", "goos: [windwos]", 1) },
			code: ProblemUnknownEnum,
		},
		{
			name: "unknown signal GOARCH",
			edit: func(s string) string { return strings.Replace(s, "goarch: []", "goarch: [quantum]", 1) },
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
		{
			name: "unknown contract GOOS",
			edit: func(s string) string {
				return strings.Replace(s, "goos: windows", "goos: windwos", 1)
			},
			code: ProblemUnknownEnum,
		},
		{
			name: "unknown contract GOARCH",
			edit: func(s string) string {
				return strings.Replace(s, "goarch: any", "goarch: quantum", 1)
			},
			code: ProblemUnknownEnum,
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

func TestRepositoryValidation(t *testing.T) {
	baseManifests := parseValidManifests(t)
	baseFacts := RepositoryFacts{
		GoPackages: map[string][]string{
			"linux": {
				"github.com/xoai/sage-wiki",
				"github.com/xoai/sage-wiki/internal/manifest",
				"github.com/xoai/sage-wiki/pkg/engine",
				"github.com/xoai/sage-wiki/tools/skillgen",
			},
		},
		Workflow: WorkflowFacts{
			Name: "CI",
			Jobs: map[string]WorkflowJob{
				"skill-drift": {},
				"ci-required": {Needs: []string{"skill-drift"}},
			},
		},
		MakeTargets:         map[string]struct{}{},
		DeterminismPackages: nil,
		PlatformSignals: []DiscoveredPlatformSignal{
			{Path: "internal/manifest/lock.go", Kind: PlatformSignalRuntimeGOOS},
		},
		ExistingPaths: map[string]struct{}{
			"skills/":                   {},
			"internal/web/dist/":        {},
			"internal/manifest/lock.go": {},
		},
	}
	if problems := validateRepository(baseManifests, baseFacts); len(problems) != 0 {
		t.Fatalf("valid repository facts: %#v", problems)
	}

	tests := []struct {
		name   string
		mutate func(*Manifests, *RepositoryFacts)
		code   ProblemCode
	}{
		{
			name: "new package is unowned",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				facts.GoPackages["linux"] = append(facts.GoPackages["linux"], "github.com/xoai/sage-wiki/internal/newpkg")
			},
			code: ProblemPackageUnowned,
		},
		{
			name: "deleted package remains declared",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				facts.GoPackages["linux"] = facts.GoPackages["linux"][:3]
			},
			code: ProblemPackageDeleted,
		},
		{
			name: "required aggregate omits job",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				facts.Workflow.Jobs["ci-required"] = WorkflowJob{}
			},
			code: ProblemAggregateMissing,
		},
		{
			name: "required aggregate has undeclared job",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				facts.Workflow.Jobs["extra"] = WorkflowJob{}
				facts.Workflow.Jobs["ci-required"] = WorkflowJob{Needs: []string{"skill-drift", "extra"}}
			},
			code: ProblemAggregateExtra,
		},
		{
			name: "required workflow job is missing",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				delete(facts.Workflow.Jobs, "skill-drift")
			},
			code: ProblemWorkflowJobMissing,
		},
		{
			name: "witness workflow reference is stale",
			mutate: func(manifests *Manifests, _ *RepositoryFacts) {
				manifests.Standards.Standards[0].Witnesses[0].Job.Workflow = "Other"
			},
			code: ProblemWorkflowMismatch,
		},
		{
			name: "aggregate workflow reference is stale",
			mutate: func(manifests *Manifests, _ *RepositoryFacts) {
				manifests.Standards.Aggregates[0].Workflow = "Other"
			},
			code: ProblemWorkflowMismatch,
		},
		{
			name: "required workflow witness is not aggregated",
			mutate: func(manifests *Manifests, _ *RepositoryFacts) {
				manifests.Standards.Standards[0].Witnesses[0].Job.Aggregate = false
			},
			code: ProblemAggregateMissing,
		},
		{
			name: "local make target is missing",
			mutate: func(manifests *Manifests, _ *RepositoryFacts) {
				target := "ci"
				manifests.Standards.Standards[0].Witnesses[0].Diagnostics.LocalMakeTarget = &target
			},
			code: ProblemMakeTargetMissing,
		},
		{
			name: "determinism package list drifts",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				facts.DeterminismPackages = []string{"internal/compiler"}
			},
			code: ProblemDeterminismDrift,
		},
		{
			name: "platform signal is unowned",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				facts.PlatformSignals = append(facts.PlatformSignals, DiscoveredPlatformSignal{Path: "internal/new.go", Kind: PlatformSignalRuntimeGOARCH})
			},
			code: ProblemPlatformUnowned,
		},
		{
			name: "platform inventory path is stale",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				delete(facts.ExistingPaths, "internal/manifest/lock.go")
			},
			code: ProblemPlatformStale,
		},
		{
			name: "generated inventory path is stale",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				delete(facts.ExistingPaths, "skills/")
			},
			code: ProblemGeneratedPathStale,
		},
		{
			name: "platform inventory signal is stale",
			mutate: func(manifests *Manifests, _ *RepositoryFacts) {
				manifests.Platforms.Inventory[0].Signals = append(manifests.Platforms.Inventory[0].Signals, PlatformSignal{Kind: PlatformSignalBuildExpression})
			},
			code: ProblemPlatformStale,
		},
		{
			name: "platform source parse fails closed",
			mutate: func(_ *Manifests, facts *RepositoryFacts) {
				facts.PlatformParseErrors = []string{"internal/broken.go: malformed build expression"}
			},
			code: ProblemPlatformParse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifests := parseValidManifests(t)
			facts := cloneRepositoryFacts(baseFacts)
			tt.mutate(&manifests, &facts)
			assertProblemCode(t, validateRepository(manifests, facts), tt.code)
		})
	}

	for _, kind := range []PlatformSignalKind{
		PlatformSignalFilenameSuffix,
		PlatformSignalBuildExpression,
		PlatformSignalRuntimeGOOS,
		PlatformSignalRuntimeGOARCH,
		PlatformSignalPlatformImport,
	} {
		t.Run("unowned signal "+string(kind), func(t *testing.T) {
			facts := cloneRepositoryFacts(baseFacts)
			facts.PlatformSignals = append(facts.PlatformSignals, DiscoveredPlatformSignal{Path: "new-signal.go", Kind: kind})
			assertProblemCode(t, validateRepository(baseManifests, facts), ProblemPlatformUnowned)
		})
	}
}

func TestPlatformSignals(t *testing.T) {
	good := fstest.MapFS{
		"suffix_linux.go": {Data: []byte("//go:build linux\n\npackage fixture\n")},
		"signals.go": {Data: []byte(`package fixture

import (
	"runtime"
	"golang.org/x/sys/unix"
)

var _, _ = runtime.GOOS, runtime.GOARCH
var _ = unix.Mmap
`)},
	}
	signals, err := scanPlatformSignals(good)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, want := range []PlatformSignalKind{
		PlatformSignalFilenameSuffix,
		PlatformSignalBuildExpression,
		PlatformSignalRuntimeGOOS,
		PlatformSignalRuntimeGOARCH,
		PlatformSignalPlatformImport,
	} {
		if !hasSignalKind(signals, want) {
			t.Errorf("signals = %#v, want kind %q", signals, want)
		}
	}

	bad := fstest.MapFS{
		"broken.go": {Data: []byte("//go:build (\n\npackage fixture\n")},
	}
	if _, err := scanPlatformSignals(bad); err == nil {
		t.Fatal("malformed build expression accepted")
	}
}

func TestPlatformFilenameSuffixRequiresConcreteGOOS(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "lock_linux.go", want: true},
		{path: "lock_windows_test.go", want: true},
		{path: "lock_windows_amd64_test.go", want: true},
		{path: "lock_hurd.go", want: true},
		{path: "lock_unix.go", want: false},
		{path: "lock_unix_test.go", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			kinds, err := scanGoFile(tt.path, []byte("package fixture\n"))
			if err != nil {
				t.Fatal(err)
			}
			_, got := kinds[PlatformSignalFilenameSuffix]
			if got != tt.want {
				t.Fatalf("filename suffix detected = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlatformBuildExpressionIncludesFeatureTags(t *testing.T) {
	kinds, err := scanGoFile("feature.go", []byte("//go:build webui\n\npackage fixture\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := kinds[PlatformSignalBuildExpression]; !ok {
		t.Fatal("non-platform build expression was not detected")
	}
}

func TestPlatformRuntimeSignalUsesImportedName(t *testing.T) {
	aliased, err := scanGoFile("aliased.go", []byte("package fixture\n\nimport goruntime \"runtime\"\n\nvar _ = goruntime.GOOS\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := aliased[PlatformSignalRuntimeGOOS]; !ok {
		t.Fatal("aliased runtime.GOOS was not detected")
	}

	shadowed, err := scanGoFile("shadowed.go", []byte("package fixture\n\ntype platform struct{ GOOS string }\n\nvar runtime platform\nvar _ = runtime.GOOS\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := shadowed[PlatformSignalRuntimeGOOS]; ok {
		t.Fatal("selector on a local runtime variable was treated as runtime.GOOS")
	}
}

func parseValidManifests(t *testing.T) Manifests {
	t.Helper()
	standards, err := parseStandards("standards.yaml", []byte(validStandards))
	if err != nil {
		t.Fatal(err)
	}
	packages, err := parsePackageOwnership("package-ownership.yaml", []byte(validOwnership))
	if err != nil {
		t.Fatal(err)
	}
	platforms, err := parsePlatformContracts("platform-contracts.yaml", []byte(validPlatforms))
	if err != nil {
		t.Fatal(err)
	}
	return Manifests{Standards: standards, Packages: packages, Platforms: platforms}
}

func cloneRepositoryFacts(in RepositoryFacts) RepositoryFacts {
	out := in
	out.GoPackages = make(map[string][]string, len(in.GoPackages))
	for goos, packages := range in.GoPackages {
		out.GoPackages[goos] = append([]string(nil), packages...)
	}
	out.Workflow.Jobs = make(map[string]WorkflowJob, len(in.Workflow.Jobs))
	for id, job := range in.Workflow.Jobs {
		job.Needs = append([]string(nil), job.Needs...)
		out.Workflow.Jobs[id] = job
	}
	out.MakeTargets = make(map[string]struct{}, len(in.MakeTargets))
	for target := range in.MakeTargets {
		out.MakeTargets[target] = struct{}{}
	}
	out.DeterminismPackages = append([]string(nil), in.DeterminismPackages...)
	out.PlatformSignals = append([]DiscoveredPlatformSignal(nil), in.PlatformSignals...)
	out.PlatformParseErrors = append([]string(nil), in.PlatformParseErrors...)
	out.ExistingPaths = make(map[string]struct{}, len(in.ExistingPaths))
	for path := range in.ExistingPaths {
		out.ExistingPaths[path] = struct{}{}
	}
	return out
}

func hasSignalKind(signals []DiscoveredPlatformSignal, want PlatformSignalKind) bool {
	for _, signal := range signals {
		if signal.Kind == want {
			return true
		}
	}
	return false
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
