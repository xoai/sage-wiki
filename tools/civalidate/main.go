package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type OwnerTier string
type WitnessStatus string
type QualificationState string
type PurposeGate string
type AuthorityCriterion string
type Trigger string
type Platform string
type PackageClass string
type PackageRole string
type PlatformSignalKind string
type DiagnosticFormat string

const (
	ProblemParse                   ProblemCode        = "parse"
	ProblemEmptyManifest           ProblemCode        = "empty-manifest"
	ProblemUnsupportedVersion      ProblemCode        = "unsupported-version"
	ProblemMissingField            ProblemCode        = "missing-field"
	ProblemUnknownEnum             ProblemCode        = "unknown-enum"
	ProblemDuplicateID             ProblemCode        = "duplicate-id"
	ProblemDanglingReference       ProblemCode        = "dangling-reference"
	ProblemGeneratedInventoryEmpty ProblemCode        = "generated-inventory-empty"
	ProblemMissingDiagnostic       ProblemCode        = "missing-diagnostic"
	ProblemPackageUnowned          ProblemCode        = "package-unowned"
	ProblemPackageDeleted          ProblemCode        = "package-deleted"
	ProblemAggregateMissing        ProblemCode        = "aggregate-missing"
	ProblemAggregateExtra          ProblemCode        = "aggregate-extra"
	ProblemWorkflowJobMissing      ProblemCode        = "workflow-job-missing"
	ProblemWorkflowMismatch        ProblemCode        = "workflow-mismatch"
	ProblemMakeTargetMissing       ProblemCode        = "make-target-missing"
	ProblemDeterminismDrift        ProblemCode        = "determinism-drift"
	ProblemGeneratedPathStale      ProblemCode        = "generated-path-stale"
	ProblemPlatformInventoryEmpty  ProblemCode        = "platform-inventory-empty"
	ProblemPlatformContractEmpty   ProblemCode        = "platform-contract-empty"
	ProblemPlatformUnowned         ProblemCode        = "platform-unowned"
	ProblemPlatformStale           ProblemCode        = "platform-stale"
	ProblemPlatformParse           ProblemCode        = "platform-parse"
	ProblemShardEmpty              ProblemCode        = "shard-empty"
	ProblemShardDuplicate          ProblemCode        = "shard-duplicate"
	ProblemShardMissing            ProblemCode        = "shard-missing"
	ProblemShardUnowned            ProblemCode        = "shard-unowned"
	ProblemServiceSelectorEmpty    ProblemCode        = "service-selector-empty"
	ProblemServiceTestDrift        ProblemCode        = "service-test-drift"
	ProblemServicePinMissing       ProblemCode        = "service-pin-missing"
	ProblemShadowJobMissing        ProblemCode        = "shadow-job-missing"
	ProblemFuzzTargetUnowned       ProblemCode        = "fuzz-target-unowned"
	ProblemFuzzTargetStale         ProblemCode        = "fuzz-target-stale"
	ProblemFuzzScheduledEmpty      ProblemCode        = "fuzz-scheduled-empty"
	PlatformSignalFilenameSuffix   PlatformSignalKind = "filename-suffix"
	PlatformSignalBuildExpression  PlatformSignalKind = "build-expression"
	PlatformSignalRuntimeGOOS      PlatformSignalKind = "runtime-goos"
	PlatformSignalRuntimeGOARCH    PlatformSignalKind = "runtime-goarch"
	PlatformSignalPlatformImport   PlatformSignalKind = "platform-import"
	PlatformSignalReviewedGeneric  PlatformSignalKind = "reviewed-generic"
)

type ProblemCode string

type Problem struct {
	Code    ProblemCode
	Path    string
	Message string
}

type Problems []Problem

type Aggregate struct {
	Workflow string `yaml:"workflow"`
	Job      string `yaml:"job"`
}

type Standard struct {
	ID            string     `yaml:"id"`
	Rationale     string     `yaml:"rationale"`
	Owner         OwnerTier  `yaml:"owner"`
	FallbackOwner *OwnerTier `yaml:"fallback_owner"`
	Witnesses     []Witness  `yaml:"witnesses"`
}

type Witness struct {
	ID            string        `yaml:"id"`
	Status        WitnessStatus `yaml:"status"`
	Command       string        `yaml:"command"`
	Triggers      []Trigger     `yaml:"triggers"`
	Platforms     []Platform    `yaml:"platforms"`
	Job           *JobRef       `yaml:"job"`
	Purpose       EvidenceGate  `yaml:"purpose"`
	Authority     EvidenceGate  `yaml:"authority"`
	AntiVacuity   string        `yaml:"anti_vacuity"`
	Runtime       Runtime       `yaml:"runtime"`
	Coverage      Coverage      `yaml:"coverage"`
	Diagnostics   Diagnostics   `yaml:"diagnostics"`
	Qualification Qualification `yaml:"qualification"`
}

type JobRef struct {
	Workflow  string `yaml:"workflow"`
	ID        string `yaml:"id"`
	Aggregate bool   `yaml:"aggregate"`
}

type EvidenceGate struct {
	Met      []string `yaml:"met"`
	Evidence string   `yaml:"evidence"`
}

type Runtime struct {
	BoundSeconds int        `yaml:"bound_seconds"`
	Recent       *RecentRun `yaml:"recent"`
}

type RecentRun struct {
	P50Seconds int `yaml:"p50_seconds"`
	P95Seconds int `yaml:"p95_seconds"`
}

type Coverage struct {
	Packages          []string       `yaml:"packages"`
	PackageClasses    []PackageClass `yaml:"package_classes"`
	PackageRoles      []PackageRole  `yaml:"package_roles"`
	PlatformContracts []string       `yaml:"platform_contracts"`
	Paths             []string       `yaml:"paths"`
	GeneratedPaths    []string       `yaml:"generated_paths"`
}

type Diagnostics struct {
	LocalCommand    *string            `yaml:"local_command"`
	LocalMakeTarget *string            `yaml:"local_make_target"`
	HostedArtifact  *string            `yaml:"hosted_artifact"`
	FailureArtifact string             `yaml:"failure_artifact"`
	Formats         []DiagnosticFormat `yaml:"formats"`
}

type Qualification struct {
	State   QualificationState `yaml:"state"`
	History []HistoryEntry     `yaml:"history"`
}

type HistoryEntry struct {
	Date         string             `yaml:"date"`
	State        QualificationState `yaml:"state"`
	Reason       string             `yaml:"reason"`
	ExitCriteria string             `yaml:"exit_criteria,omitempty"`
}

type StandardsManifest struct {
	SchemaVersion int         `yaml:"schema_version"`
	Aggregates    []Aggregate `yaml:"aggregates"`
	Standards     []Standard  `yaml:"standards"`
}

type PackageOwnership struct {
	Path  string        `yaml:"path"`
	Class PackageClass  `yaml:"class"`
	Owner string        `yaml:"owner"`
	Roles []PackageRole `yaml:"roles"`
}

type PackageOwnershipManifest struct {
	SchemaVersion   int                `yaml:"schema_version"`
	Module          string             `yaml:"module"`
	Packages        []PackageOwnership `yaml:"packages"`
	LinuxRaceShards *LinuxRaceShards   `yaml:"linux_race_shards"`
}

type Shard struct {
	ID       string   `yaml:"id"`
	Packages []string `yaml:"packages"`
}

type LinuxRaceShards struct {
	Workflow string  `yaml:"workflow"`
	Shards   []Shard `yaml:"shards"`
}

type ServiceImage struct {
	Repository string `yaml:"repository"`
	Tag        string `yaml:"tag"`
	Digest     string `yaml:"digest"`
}

type ServiceClient struct {
	Name   string `yaml:"name"`
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
}

type ServiceContract struct {
	ID            string         `yaml:"id"`
	Workflow      string         `yaml:"workflow"`
	Job           string         `yaml:"job"`
	Image         ServiceImage   `yaml:"image"`
	Client        *ServiceClient `yaml:"client"`
	Env           string         `yaml:"env"`
	Packages      []string       `yaml:"packages"`
	SelectedTests []string       `yaml:"selected_tests"`
	Parallel      int            `yaml:"parallel"`
}

type ServiceContractsManifest struct {
	SchemaVersion int               `yaml:"schema_version"`
	Services      []ServiceContract `yaml:"services"`
}

type FuzzTarget struct {
	Package       string `yaml:"package"`
	Target        string `yaml:"target"`
	Tier          string `yaml:"tier"`
	BudgetSeconds int    `yaml:"budget_seconds"`
}

type FuzzTargetsManifest struct {
	SchemaVersion int          `yaml:"schema_version"`
	Targets       []FuzzTarget `yaml:"targets"`
}

type DiscoveredFuzzTarget struct {
	Package string
	Target  string
}

type PlatformContract struct {
	ID        string   `yaml:"id"`
	Witness   string   `yaml:"witness"`
	Package   string   `yaml:"package"`
	GOOS      string   `yaml:"goos"`
	GOARCH    string   `yaml:"goarch"`
	Invariant string   `yaml:"invariant"`
	Tests     []string `yaml:"tests"`
}

type PlatformSignal struct {
	Kind   PlatformSignalKind `yaml:"kind"`
	GOOS   []string           `yaml:"goos"`
	GOARCH []string           `yaml:"goarch"`
}

type PlatformInventoryItem struct {
	Path      string           `yaml:"path"`
	Signals   []PlatformSignal `yaml:"signals"`
	Contracts []string         `yaml:"contracts"`
	Reason    string           `yaml:"reason"`
}

type PlatformContractsManifest struct {
	SchemaVersion int                     `yaml:"schema_version"`
	FocusedGOOS   []string                `yaml:"focused_goos"`
	Contracts     []PlatformContract      `yaml:"contracts"`
	Inventory     []PlatformInventoryItem `yaml:"inventory"`
}

type Manifests struct {
	Standards StandardsManifest
	Packages  PackageOwnershipManifest
	Platforms PlatformContractsManifest
	Services  ServiceContractsManifest
	Fuzz      FuzzTargetsManifest
}

type WorkflowJob struct {
	Needs []string
}

type WorkflowFacts struct {
	Name string
	Jobs map[string]WorkflowJob
}

type DiscoveredPlatformSignal struct {
	Path string
	Kind PlatformSignalKind
}

type RepositoryFacts struct {
	GoPackages          map[string][]string
	Workflow            WorkflowFacts
	WorkflowRaw         string
	MakeTargets         map[string]struct{}
	DeterminismPackages []string
	PlatformSignals     []DiscoveredPlatformSignal
	PlatformParseErrors []string
	ExistingPaths       map[string]struct{}
	ExpectedWorkflows   map[string]struct{}
	ServiceTests        map[string][]string
	FuzzTargets         []DiscoveredFuzzTarget
}

var (
	idPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	shaPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	checksumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ownerTiers      = setOf(
		"merge-required-ci", "pr-advisory", "scheduled-diagnostics",
		"release-certification", "local-pre-push", "human-review",
	)
	witnessStatuses     = setOf("required", "advisory", "nonblocking", "publication-blocking", "local-required", "review-required")
	qualificationStates = setOf("candidate", "qualifying", "required-requalifying", "required", "incident", "demoted", "not-applicable")
	triggers            = setOf("pull-request", "merge-group", "main-push", "schedule", "workflow-run", "tag", "manual")
	platforms           = setOf("linux", "darwin", "windows", "node22-alpine", "release-targets", "maintainer-workstation", "human-review")
	packageClasses      = setOf("test-owned", "compile-only", "example-smoke", "helper-library")
	packageRoles        = setOf("determinism-writer")
	signalKinds         = setOf(
		string(PlatformSignalFilenameSuffix), string(PlatformSignalBuildExpression),
		string(PlatformSignalRuntimeGOOS), string(PlatformSignalRuntimeGOARCH),
		string(PlatformSignalPlatformImport), string(PlatformSignalReviewedGeneric),
	)
	diagnosticFormats = setOf("text", "diff", "go-test-json", "github-annotation", "sarif", "service-logs", "review-record", "provenance", "benchmark-data")
	fuzzTiers         = setOf("pr", "scheduled")
	goosNames         = setOf("aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "unix")
	filenameGOOSNames = setOf("aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows")
	goarchNames       = setOf("386", "amd64", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x", "wasm")
)

func parseStandards(name string, data []byte) (StandardsManifest, error) {
	return parseManifest[StandardsManifest](name, data)
}

func parsePackageOwnership(name string, data []byte) (PackageOwnershipManifest, error) {
	return parseManifest[PackageOwnershipManifest](name, data)
}

func parsePlatformContracts(name string, data []byte) (PlatformContractsManifest, error) {
	return parseManifest[PlatformContractsManifest](name, data)
}

func parseServiceContracts(name string, data []byte) (ServiceContractsManifest, error) {
	return parseManifest[ServiceContractsManifest](name, data)
}

func parseFuzzTargets(name string, data []byte) (FuzzTargetsManifest, error) {
	return parseManifest[FuzzTargetsManifest](name, data)
}

func parseManifest[T interface {
	StandardsManifest | PackageOwnershipManifest | PlatformContractsManifest | ServiceContractsManifest | FuzzTargetsManifest
}](name string, data []byte) (T, error) {
	var out T
	if len(bytes.TrimSpace(data)) == 0 {
		return out, fmt.Errorf("%s: empty manifest", name)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("%s: %w", name, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return out, fmt.Errorf("%s: multiple YAML documents", name)
		}
		return out, fmt.Errorf("%s: trailing document: %w", name, err)
	}
	if manifestVersion(out) != 1 {
		return out, fmt.Errorf("%s: unsupported schema_version %d", name, manifestVersion(out))
	}
	return out, nil
}

func manifestVersion[T interface {
	StandardsManifest | PackageOwnershipManifest | PlatformContractsManifest | ServiceContractsManifest | FuzzTargetsManifest
}](m T) int {
	switch v := any(m).(type) {
	case StandardsManifest:
		return v.SchemaVersion
	case PackageOwnershipManifest:
		return v.SchemaVersion
	case PlatformContractsManifest:
		return v.SchemaVersion
	case ServiceContractsManifest:
		return v.SchemaVersion
	case FuzzTargetsManifest:
		return v.SchemaVersion
	default:
		return 0
	}
}

func validateStatic(m Manifests) Problems {
	var problems Problems
	witnessIDs := map[string]struct{}{}
	standardIDs := map[string]struct{}{}
	generatedPaths := 0
	for si, standard := range m.Standards.Standards {
		path := fmt.Sprintf("standards[%d]", si)
		require(&problems, path+".id", standard.ID)
		require(&problems, path+".rationale", standard.Rationale)
		checkEnum(&problems, path+".owner", string(standard.Owner), ownerTiers)
		if standard.FallbackOwner != nil {
			checkEnum(&problems, path+".fallback_owner", string(*standard.FallbackOwner), ownerTiers)
		}
		if _, exists := standardIDs[standard.ID]; exists {
			problems.add(ProblemDuplicateID, path+".id", "duplicate standard id "+standard.ID)
		}
		standardIDs[standard.ID] = struct{}{}
		if standard.ID != "" && !idPattern.MatchString(standard.ID) {
			problems.add(ProblemUnknownEnum, path+".id", "invalid id")
		}
		for wi, witness := range standard.Witnesses {
			wpath := fmt.Sprintf("%s.witnesses[%d]", path, wi)
			require(&problems, wpath+".id", witness.ID)
			checkEnum(&problems, wpath+".status", string(witness.Status), witnessStatuses)
			require(&problems, wpath+".command", witness.Command)
			if _, exists := witnessIDs[witness.ID]; exists {
				problems.add(ProblemDuplicateID, wpath+".id", "duplicate witness id "+witness.ID)
			}
			witnessIDs[witness.ID] = struct{}{}
			for i, trigger := range witness.Triggers {
				checkEnum(&problems, fmt.Sprintf("%s.triggers[%d]", wpath, i), string(trigger), triggers)
			}
			for i, platform := range witness.Platforms {
				checkEnum(&problems, fmt.Sprintf("%s.platforms[%d]", wpath, i), string(platform), platforms)
			}
			require(&problems, wpath+".purpose.evidence", witness.Purpose.Evidence)
			require(&problems, wpath+".authority.evidence", witness.Authority.Evidence)
			require(&problems, wpath+".anti_vacuity", witness.AntiVacuity)
			checkEnum(&problems, wpath+".qualification.state", string(witness.Qualification.State), qualificationStates)
			for i, format := range witness.Diagnostics.Formats {
				checkEnum(&problems, fmt.Sprintf("%s.diagnostics.formats[%d]", wpath, i), string(format), diagnosticFormats)
			}
			for i, class := range witness.Coverage.PackageClasses {
				checkEnum(&problems, fmt.Sprintf("%s.coverage.package_classes[%d]", wpath, i), string(class), packageClasses)
			}
			for i, role := range witness.Coverage.PackageRoles {
				checkEnum(&problems, fmt.Sprintf("%s.coverage.package_roles[%d]", wpath, i), string(role), packageRoles)
			}
			generatedPaths += len(witness.Coverage.GeneratedPaths)
			if witness.Status == "required" && witness.Diagnostics.LocalCommand == nil && witness.Diagnostics.HostedArtifact == nil {
				problems.add(ProblemMissingDiagnostic, wpath+".diagnostics", "required witness needs a local command or hosted artifact")
			}
		}
	}
	if _, hasGenerated := standardIDs["generated-contract-drift"]; hasGenerated && generatedPaths == 0 {
		problems.add(ProblemGeneratedInventoryEmpty, "standards.generated-contract-drift", "generated path inventory is empty")
	}

	packagePaths := map[string]struct{}{}
	packageClass := map[string]PackageClass{}
	for i, pkg := range m.Packages.Packages {
		path := fmt.Sprintf("packages[%d]", i)
		require(&problems, path+".path", pkg.Path)
		checkEnum(&problems, path+".class", string(pkg.Class), packageClasses)
		if _, exists := packagePaths[pkg.Path]; exists {
			problems.add(ProblemDuplicateID, path+".path", "duplicate package "+pkg.Path)
		}
		packagePaths[pkg.Path] = struct{}{}
		packageClass[pkg.Path] = pkg.Class
		if _, exists := witnessIDs[pkg.Owner]; len(witnessIDs) > 0 && !exists {
			problems.add(ProblemDanglingReference, path+".owner", "unknown witness "+pkg.Owner)
		}
		for ri, role := range pkg.Roles {
			checkEnum(&problems, fmt.Sprintf("%s.roles[%d]", path, ri), string(role), packageRoles)
		}
	}

	validateShardsStatic(&problems, m, packagePaths, packageClass)
	validateServicesStatic(&problems, m, packagePaths)
	validateFuzzStatic(&problems, m, packagePaths)

	contractIDs := map[string]struct{}{}
	focused := map[string]int{}
	for i, goos := range m.Platforms.FocusedGOOS {
		if _, ok := goosNames[goos]; !ok || goos == "unix" {
			problems.add(ProblemUnknownEnum, fmt.Sprintf("focused_goos[%d]", i), "unknown GOOS "+goos)
		}
		focused[goos] = 0
	}
	if m.Platforms.SchemaVersion != 0 && len(m.Platforms.FocusedGOOS) == 0 {
		problems.add(ProblemPlatformContractEmpty, "focused_goos", "focused GOOS list is empty")
	}
	for i, contract := range m.Platforms.Contracts {
		path := fmt.Sprintf("contracts[%d]", i)
		if _, exists := contractIDs[contract.ID]; exists {
			problems.add(ProblemDuplicateID, path+".id", "duplicate platform contract "+contract.ID)
		}
		contractIDs[contract.ID] = struct{}{}
		if _, exists := goosNames[contract.GOOS]; !exists || contract.GOOS == "unix" {
			problems.add(ProblemUnknownEnum, path+".goos", "unknown GOOS "+contract.GOOS)
		}
		if contract.GOARCH != "any" {
			if _, exists := goarchNames[contract.GOARCH]; !exists {
				problems.add(ProblemUnknownEnum, path+".goarch", "unknown GOARCH "+contract.GOARCH)
			}
		}
		if _, exists := witnessIDs[contract.Witness]; len(witnessIDs) > 0 && !exists {
			problems.add(ProblemDanglingReference, path+".witness", "unknown witness "+contract.Witness)
		}
		if _, exists := packagePaths[contract.Package]; len(packagePaths) > 0 && !exists {
			problems.add(ProblemDanglingReference, path+".package", "unknown package "+contract.Package)
		}
		if _, exists := focused[contract.GOOS]; exists {
			focused[contract.GOOS]++
		}
		if len(contract.Tests) == 0 {
			problems.add(ProblemPlatformContractEmpty, path+".tests", "platform contract has no tests")
		}
		require(&problems, path+".invariant", contract.Invariant)
	}
	for goos, count := range focused {
		if count == 0 {
			problems.add(ProblemPlatformContractEmpty, "contracts", "no focused contract for "+goos)
		}
	}
	if m.Platforms.SchemaVersion != 0 && len(m.Platforms.Inventory) == 0 {
		problems.add(ProblemPlatformInventoryEmpty, "inventory", "platform inventory is empty")
	}
	for i, item := range m.Platforms.Inventory {
		path := fmt.Sprintf("inventory[%d]", i)
		require(&problems, path+".path", item.Path)
		require(&problems, path+".reason", item.Reason)
		for si, signal := range item.Signals {
			signalPath := fmt.Sprintf("%s.signals[%d]", path, si)
			checkEnum(&problems, signalPath+".kind", string(signal.Kind), signalKinds)
			for gi, goos := range signal.GOOS {
				if _, exists := filenameGOOSNames[goos]; !exists {
					problems.add(ProblemUnknownEnum, fmt.Sprintf("%s.goos[%d]", signalPath, gi), "unknown GOOS "+goos)
				}
			}
			for ai, goarch := range signal.GOARCH {
				if _, exists := goarchNames[goarch]; !exists {
					problems.add(ProblemUnknownEnum, fmt.Sprintf("%s.goarch[%d]", signalPath, ai), "unknown GOARCH "+goarch)
				}
			}
		}
		for ci, id := range item.Contracts {
			if _, exists := contractIDs[id]; !exists {
				problems.add(ProblemDanglingReference, fmt.Sprintf("%s.contracts[%d]", path, ci), "unknown platform contract "+id)
			}
		}
	}
	for si, standard := range m.Standards.Standards {
		for wi, witness := range standard.Witnesses {
			for ci, id := range witness.Coverage.PlatformContracts {
				if len(contractIDs) > 0 {
					if _, exists := contractIDs[id]; !exists {
						problems.add(ProblemDanglingReference, fmt.Sprintf("standards[%d].witnesses[%d].coverage.platform_contracts[%d]", si, wi, ci), "unknown platform contract "+id)
					}
				}
			}
		}
	}
	problems.sort()
	return problems
}

func validateRepository(m Manifests, facts RepositoryFacts) Problems {
	var problems Problems
	declared := map[string]int{}
	for _, pkg := range m.Packages.Packages {
		full := m.Packages.Module
		if pkg.Path != "." {
			full += "/" + pkg.Path
		}
		declared[full]++
	}
	actual := map[string]struct{}{}
	for _, packages := range facts.GoPackages {
		for _, pkg := range packages {
			actual[pkg] = struct{}{}
		}
	}
	for pkg := range actual {
		if declared[pkg] == 0 {
			problems.add(ProblemPackageUnowned, "packages", "unowned package "+pkg)
		}
	}
	for pkg := range declared {
		if _, exists := actual[pkg]; !exists {
			problems.add(ProblemPackageDeleted, "packages", "declared package does not exist "+pkg)
		}
	}

	requiredJobs := map[string]struct{}{}
	known := knownWorkflowNames(m, facts)
	for _, standard := range m.Standards.Standards {
		for _, witness := range standard.Witnesses {
			if witness.Job != nil {
				if _, declared := known[witness.Job.Workflow]; !declared {
					problems.add(ProblemWorkflowMismatch, "workflow.name", fmt.Sprintf("witness %s references undeclared workflow %s", witness.ID, witness.Job.Workflow))
				} else if witness.Job.Workflow == facts.Workflow.Name {
					if _, exists := facts.Workflow.Jobs[witness.Job.ID]; !exists {
						problems.add(ProblemWorkflowJobMissing, "workflow.jobs", "missing workflow job "+witness.Job.ID)
					}
					if witness.Status == "required" {
						if witness.Job.Aggregate {
							requiredJobs[witness.Job.ID] = struct{}{}
						} else {
							problems.add(ProblemAggregateMissing, "standards.witnesses."+witness.ID, "required workflow witness is not declared aggregate-owned")
						}
					}
				}
			}
			if witness.Diagnostics.LocalMakeTarget != nil {
				if _, exists := facts.MakeTargets[*witness.Diagnostics.LocalMakeTarget]; !exists {
					problems.add(ProblemMakeTargetMissing, "makefile", "missing target "+*witness.Diagnostics.LocalMakeTarget)
				}
			}
		}
	}
	for _, aggregate := range m.Standards.Aggregates {
		if _, declared := known[aggregate.Workflow]; !declared {
			problems.add(ProblemWorkflowMismatch, "workflow.name", fmt.Sprintf("aggregate references undeclared workflow %s", aggregate.Workflow))
			continue
		}
		if aggregate.Workflow != facts.Workflow.Name {
			continue
		}
		job, exists := facts.Workflow.Jobs[aggregate.Job]
		if !exists {
			problems.add(ProblemWorkflowJobMissing, "workflow.jobs", "missing aggregate job "+aggregate.Job)
			continue
		}
		needs := sliceSet(job.Needs)
		for id := range requiredJobs {
			if _, exists := needs[id]; !exists {
				problems.add(ProblemAggregateMissing, "workflow.jobs."+aggregate.Job, "aggregate omits "+id)
			}
		}
		for id := range needs {
			if _, exists := requiredJobs[id]; !exists {
				problems.add(ProblemAggregateExtra, "workflow.jobs."+aggregate.Job, "aggregate includes undeclared job "+id)
			}
		}
	}

	if shards := m.Packages.LinuxRaceShards; shards != nil {
		if _, declared := known[shards.Workflow]; !declared {
			problems.add(ProblemWorkflowMismatch, "linux_race_shards.workflow", "shards reference undeclared workflow "+shards.Workflow)
		} else if shards.Workflow == facts.Workflow.Name {
			for _, shard := range shards.Shards {
				if _, exists := facts.Workflow.Jobs[shard.ID]; !exists {
					problems.add(ProblemShadowJobMissing, "workflow.jobs", "missing shard job "+shard.ID)
				}
			}
		}
	}
	for _, service := range m.Services.Services {
		if _, declared := known[service.Workflow]; !declared {
			problems.add(ProblemWorkflowMismatch, "services."+service.ID, "service references undeclared workflow "+service.Workflow)
			continue
		}
		if service.Workflow != facts.Workflow.Name {
			continue
		}
		if _, exists := facts.Workflow.Jobs[service.Job]; !exists {
			problems.add(ProblemShadowJobMissing, "workflow.jobs", "missing service job "+service.Job)
		}
		if !strings.Contains(facts.WorkflowRaw, service.Image.Digest) {
			problems.add(ProblemServicePinMissing, "workflow.jobs."+service.Job, "workflow does not use the pinned image digest for "+service.ID)
		}
		if service.Client != nil && !strings.Contains(facts.WorkflowRaw, service.Client.SHA256) {
			problems.add(ProblemServicePinMissing, "workflow.jobs."+service.Job, "workflow does not use the pinned client checksum for "+service.ID)
		}
	}
	if facts.ServiceTests != nil {
		for _, service := range m.Services.Services {
			selected := sliceSet(service.SelectedTests)
			found := sliceSet(facts.ServiceTests[service.ID])
			for _, test := range service.SelectedTests {
				if _, exists := found[test]; !exists {
					problems.add(ProblemServiceTestDrift, "services."+service.ID, "selected test not discovered: "+test)
				}
			}
			for _, test := range facts.ServiceTests[service.ID] {
				if _, exists := selected[test]; !exists {
					problems.add(ProblemServiceTestDrift, "services."+service.ID, "discovered test not selected: "+test)
				}
			}
			if len(service.SelectedTests) > 0 && len(facts.ServiceTests[service.ID]) == 0 {
				problems.add(ProblemServiceTestDrift, "services."+service.ID, "service test discovery returned no tests")
			}
		}
	}

	if facts.FuzzTargets != nil {
		inventory := map[string]struct{}{}
		scheduled := 0
		for _, target := range m.Fuzz.Targets {
			inventory[target.Package+"|"+target.Target] = struct{}{}
			if target.Tier == "scheduled" {
				scheduled++
			}
		}
		for _, target := range facts.FuzzTargets {
			if _, exists := inventory[target.Package+"|"+target.Target]; !exists {
				problems.add(ProblemFuzzTargetUnowned, "fuzz_targets", "source fuzz target missing from inventory: "+target.Package+" "+target.Target)
			}
		}
		source := map[string]struct{}{}
		for _, target := range facts.FuzzTargets {
			source[target.Package+"|"+target.Target] = struct{}{}
		}
		for _, target := range m.Fuzz.Targets {
			if _, exists := source[target.Package+"|"+target.Target]; !exists {
				problems.add(ProblemFuzzTargetStale, "fuzz_targets", "inventory fuzz target no longer in source: "+target.Package+" "+target.Target)
			}
		}
		if len(m.Fuzz.Targets) > 0 && scheduled == 0 {
			problems.add(ProblemFuzzScheduledEmpty, "fuzz_targets", "scheduled fuzz suite is empty")
		}
	}

	if facts.DeterminismPackages != nil {
		var manifestPackages []string
		for _, pkg := range m.Packages.Packages {
			for _, role := range pkg.Roles {
				if role == "determinism-writer" {
					manifestPackages = append(manifestPackages, pkg.Path)
				}
			}
		}
		sort.Strings(manifestPackages)
		scriptPackages := append([]string(nil), facts.DeterminismPackages...)
		sort.Strings(scriptPackages)
		if strings.Join(manifestPackages, "\n") != strings.Join(scriptPackages, "\n") {
			problems.add(ProblemDeterminismDrift, "packages.roles", "determinism-writer roles differ from the script")
		}
	}

	inventory := map[string]map[PlatformSignalKind]struct{}{}
	discovered := map[string]map[PlatformSignalKind]struct{}{}
	for _, signal := range facts.PlatformSignals {
		if discovered[signal.Path] == nil {
			discovered[signal.Path] = map[PlatformSignalKind]struct{}{}
		}
		discovered[signal.Path][signal.Kind] = struct{}{}
	}
	for _, item := range m.Platforms.Inventory {
		if _, exists := facts.ExistingPaths[item.Path]; !exists {
			problems.add(ProblemPlatformStale, "platform.inventory", "stale inventory path "+item.Path)
		}
		inventory[item.Path] = map[PlatformSignalKind]struct{}{}
		for _, signal := range item.Signals {
			inventory[item.Path][signal.Kind] = struct{}{}
			if signal.Kind == PlatformSignalReviewedGeneric {
				continue
			}
			if _, exists := discovered[item.Path][signal.Kind]; !exists {
				problems.add(ProblemPlatformStale, "platform.inventory", fmt.Sprintf("stale %s signal in %s", signal.Kind, item.Path))
			}
		}
	}
	for _, signal := range facts.PlatformSignals {
		kinds, exists := inventory[signal.Path]
		if !exists {
			problems.add(ProblemPlatformUnowned, "platform.inventory", "unowned platform signal "+signal.Path)
			continue
		}
		if _, exists := kinds[signal.Kind]; !exists {
			problems.add(ProblemPlatformUnowned, "platform.inventory", fmt.Sprintf("unowned %s signal in %s", signal.Kind, signal.Path))
		}
	}
	for _, parseError := range facts.PlatformParseErrors {
		problems.add(ProblemPlatformParse, "platform.scan", parseError)
	}
	for _, standard := range m.Standards.Standards {
		for _, witness := range standard.Witnesses {
			for _, path := range witness.Coverage.GeneratedPaths {
				if _, exists := facts.ExistingPaths[path]; !exists {
					problems.add(ProblemGeneratedPathStale, "generated-paths", "declared generated path does not exist "+path)
				}
			}
		}
	}
	problems.sort()
	return problems
}

func knownWorkflowNames(m Manifests, facts RepositoryFacts) map[string]struct{} {
	names := map[string]struct{}{facts.Workflow.Name: {}}
	for name := range facts.ExpectedWorkflows {
		names[name] = struct{}{}
	}
	return names
}

func validateShardsStatic(problems *Problems, m Manifests, packagePaths map[string]struct{}, packageClass map[string]PackageClass) {
	shards := m.Packages.LinuxRaceShards
	if shards == nil {
		return
	}
	require(problems, "linux_race_shards.workflow", shards.Workflow)
	shardIDs := map[string]struct{}{}
	covered := map[string]struct{}{}
	for i, shard := range shards.Shards {
		path := fmt.Sprintf("linux_race_shards.shards[%d]", i)
		require(problems, path+".id", shard.ID)
		if _, exists := shardIDs[shard.ID]; exists {
			problems.add(ProblemDuplicateID, path+".id", "duplicate shard "+shard.ID)
		}
		shardIDs[shard.ID] = struct{}{}
		if len(shard.Packages) == 0 {
			problems.add(ProblemShardEmpty, path+".packages", "shard "+shard.ID+" has no packages")
		}
		for _, pkg := range shard.Packages {
			if _, exists := covered[pkg]; exists {
				problems.add(ProblemShardDuplicate, path+".packages", "package in more than one shard: "+pkg)
			}
			covered[pkg] = struct{}{}
			class, exists := packageClass[pkg]
			if len(packagePaths) > 0 && !exists {
				problems.add(ProblemShardUnowned, path+".packages", "shard package is not owned: "+pkg)
				continue
			}
			if class != "test-owned" && class != "helper-library" {
				problems.add(ProblemShardUnowned, path+".packages", "shard package is not ordinary test-owned code: "+pkg)
			}
		}
	}
	if len(packagePaths) > 0 {
		for pkg, class := range packageClass {
			if class != "test-owned" && class != "helper-library" {
				continue
			}
			if _, exists := covered[pkg]; !exists {
				problems.add(ProblemShardMissing, "linux_race_shards", "ordinary package is in no shard: "+pkg)
			}
		}
	}
}

func validateServicesStatic(problems *Problems, m Manifests, packagePaths map[string]struct{}) {
	serviceIDs := map[string]struct{}{}
	for i, service := range m.Services.Services {
		path := fmt.Sprintf("services[%d]", i)
		require(problems, path+".id", service.ID)
		if _, exists := serviceIDs[service.ID]; exists {
			problems.add(ProblemDuplicateID, path+".id", "duplicate service "+service.ID)
		}
		serviceIDs[service.ID] = struct{}{}
		require(problems, path+".workflow", service.Workflow)
		require(problems, path+".job", service.Job)
		require(problems, path+".env", service.Env)
		require(problems, path+".image.repository", service.Image.Repository)
		if !shaPattern.MatchString(service.Image.Digest) {
			problems.add(ProblemServicePinMissing, path+".image.digest", "service image is not pinned by sha256 digest")
		}
		if service.Client != nil {
			require(problems, path+".client.name", service.Client.Name)
			require(problems, path+".client.url", service.Client.URL)
			if !checksumPattern.MatchString(service.Client.SHA256) {
				problems.add(ProblemServicePinMissing, path+".client.sha256", "service client is not pinned by sha256 checksum")
			}
		}
		if len(service.Packages) == 0 {
			problems.add(ProblemServiceSelectorEmpty, path+".packages", "service has no packages")
		}
		for _, pkg := range service.Packages {
			if _, exists := packagePaths[pkg]; len(packagePaths) > 0 && !exists {
				problems.add(ProblemDanglingReference, path+".packages", "unknown package "+pkg)
			}
		}
		if len(service.SelectedTests) == 0 {
			problems.add(ProblemServiceSelectorEmpty, path+".selected_tests", "service selector is empty (tests would silently skip)")
		}
	}
}

func validateFuzzStatic(problems *Problems, m Manifests, packagePaths map[string]struct{}) {
	pairs := map[string]struct{}{}
	for i, target := range m.Fuzz.Targets {
		path := fmt.Sprintf("fuzz_targets[%d]", i)
		require(problems, path+".package", target.Package)
		require(problems, path+".target", target.Target)
		checkEnum(problems, path+".tier", target.Tier, fuzzTiers)
		if target.BudgetSeconds <= 0 {
			problems.add(ProblemMissingField, path+".budget_seconds", "fuzz target budget must be positive")
		}
		pair := target.Package + "|" + target.Target
		if _, exists := pairs[pair]; exists {
			problems.add(ProblemDuplicateID, path, "duplicate fuzz target "+pair)
		}
		pairs[pair] = struct{}{}
		if _, exists := packagePaths[target.Package]; len(packagePaths) > 0 && !exists && target.Package != "" {
			problems.add(ProblemDanglingReference, path+".package", "unknown package "+target.Package)
		}
	}
}

func listGoPackages(ctx context.Context, root string, platforms []string) (map[string][]string, error) {
	out := make(map[string][]string, len(platforms))
	for _, goos := range platforms {
		cmd := exec.CommandContext(ctx, "go", "list", "./...")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0")
		b, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list for %s: %w", goos, err)
		}
		out[goos] = strings.Fields(string(b))
	}
	return out, nil
}

func parseWorkflow(name string, data []byte) (WorkflowFacts, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return WorkflowFacts{}, fmt.Errorf("%s: %w", name, err)
	}
	root, err := mappingRoot(&node)
	if err != nil {
		return WorkflowFacts{}, fmt.Errorf("%s: %w", name, err)
	}
	facts := WorkflowFacts{Jobs: map[string]WorkflowJob{}}
	if value := mappingValue(root, "name"); value != nil {
		facts.Name = value.Value
	}
	jobs := mappingValue(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return WorkflowFacts{}, fmt.Errorf("%s: jobs mapping missing", name)
	}
	for i := 0; i < len(jobs.Content); i += 2 {
		id := jobs.Content[i].Value
		jobNode := jobs.Content[i+1]
		job := WorkflowJob{}
		if needs := mappingValue(jobNode, "needs"); needs != nil {
			switch needs.Kind {
			case yaml.ScalarNode:
				job.Needs = []string{needs.Value}
			case yaml.SequenceNode:
				for _, item := range needs.Content {
					job.Needs = append(job.Needs, item.Value)
				}
			default:
				return WorkflowFacts{}, fmt.Errorf("%s: jobs.%s.needs must be scalar or sequence", name, id)
			}
		}
		facts.Jobs[id] = job
	}
	return facts, nil
}

func parseMakeTargets(data []byte) (map[string]struct{}, error) {
	targets := map[string]struct{}{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	re := regexp.MustCompile(`^([A-Za-z0-9_.-]+(?:\s+[A-Za-z0-9_.-]+)*)\s*:`)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		match := re.FindStringSubmatch(line)
		if len(match) == 2 {
			for _, target := range strings.Fields(match[1]) {
				targets[target] = struct{}{}
			}
		}
	}
	return targets, scanner.Err()
}

func parseDeterminismPackages(data []byte) ([]string, error) {
	re := regexp.MustCompile(`(?m)^PKGS="([^"]+)"`)
	match := re.FindSubmatch(data)
	if len(match) != 2 {
		return nil, errors.New("PKGS declaration missing")
	}
	packages := strings.Fields(string(match[1]))
	if len(packages) == 0 {
		return nil, errors.New("PKGS declaration empty")
	}
	return packages, nil
}

func scanPlatformSignals(fsys fs.FS) ([]DiscoveredPlatformSignal, error) {
	var signals []DiscoveredPlatformSignal
	err := fs.WalkDir(fsys, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != "." && shouldSkipDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		kinds, err := scanGoFile(path, data)
		if err != nil {
			return err
		}
		for kind := range kinds {
			signals = append(signals, DiscoveredPlatformSignal{Path: filepath.ToSlash(path), Kind: kind})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Path == signals[j].Path {
			return signals[i].Kind < signals[j].Kind
		}
		return signals[i].Path < signals[j].Path
	})
	return signals, nil
}

func scanGoFile(path string, data []byte) (map[PlatformSignalKind]struct{}, error) {
	kinds := map[PlatformSignalKind]struct{}{}
	base := strings.TrimSuffix(filepath.Base(path), ".go")
	parts := strings.Split(base, "_")
	if len(parts) > 1 {
		end := len(parts)
		if parts[end-1] == "test" {
			end--
		}
		if end > 1 {
			suffix := parts[end-1]
			if _, ok := filenameGOOSNames[suffix]; ok {
				kinds[PlatformSignalFilenameSuffix] = struct{}{}
			}
			if _, ok := goarchNames[suffix]; ok {
				kinds[PlatformSignalFilenameSuffix] = struct{}{}
			}
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//go:build") {
			_, err := constraint.Parse(line)
			if err != nil {
				return nil, fmt.Errorf("%s: malformed build expression: %w", path, err)
			}
			kinds[PlatformSignalBuildExpression] = struct{}{}
			break
		}
		if line != "" && !strings.HasPrefix(line, "//") {
			break
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, data, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: parse: %w", path, err)
	}
	runtimeNames := map[string]struct{}{}
	for _, imp := range file.Imports {
		value := strings.Trim(imp.Path.Value, `"`)
		if value == "syscall" || strings.HasPrefix(value, "golang.org/x/sys/") || value == "github.com/zalando/go-keyring" {
			kinds[PlatformSignalPlatformImport] = struct{}{}
		}
		if value == "runtime" {
			name := "runtime"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if name != "_" && name != "." {
				runtimeNames[name] = struct{}{}
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, imported := runtimeNames[ident.Name]; !imported {
			return true
		}
		switch selector.Sel.Name {
		case "GOOS":
			kinds[PlatformSignalRuntimeGOOS] = struct{}{}
		case "GOARCH":
			kinds[PlatformSignalRuntimeGOARCH] = struct{}{}
		}
		return true
	})
	return kinds, nil
}

var fuzzFuncPattern = regexp.MustCompile(`^func (Fuzz[A-Za-z0-9_]+)\(`)

func scanFuzzTargets(fsys fs.FS) ([]DiscoveredFuzzTarget, error) {
	var targets []DiscoveredFuzzTarget
	err := fs.WalkDir(fsys, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != "." && shouldSkipDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		for _, line := range strings.Split(string(data), "\n") {
			match := fuzzFuncPattern.FindStringSubmatch(strings.TrimSpace(line))
			if match != nil {
				targets = append(targets, DiscoveredFuzzTarget{Package: dir, Target: match[1]})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Package != targets[j].Package {
			return targets[i].Package < targets[j].Package
		}
		return targets[i].Target < targets[j].Target
	})
	return targets, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("civalidate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	standardsPath := flags.String("standards", "ci/standards.yaml", "standards manifest")
	packagesPath := flags.String("packages", "ci/package-ownership.yaml", "package ownership manifest")
	platformsPath := flags.String("platforms", "ci/platform-contracts.yaml", "platform contracts manifest")
	servicesPath := flags.String("services", "ci/service-contracts.yaml", "service contracts manifest")
	fuzzTargetsPath := flags.String("fuzz-targets", "ci/fuzz-targets.yaml", "fuzz targets manifest")
	workflowPath := flags.String("workflow", ".github/workflows/ci.yml", "workflow file")
	makefilePath := flags.String("makefile", "Makefile", "Makefile")
	printShard := flags.String("print-shard", "", "print the go test package list for a shard and exit")
	printPlatformPackages := flags.String("print-platform-packages", "", "print the focused contract package list for a GOOS and exit")
	printServiceRun := flags.String("print-service-run", "", "print the focused go test invocation parts for a service and exit")
	printFuzzTargetsJSON := flags.String("print-fuzz-targets-json", "", "print fuzz targets for a tier (pr, scheduled, or all) as JSON and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	standardsData, err := os.ReadFile(*standardsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	packagesData, err := os.ReadFile(*packagesPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	platformsData, err := os.ReadFile(*platformsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	servicesData, err := os.ReadFile(*servicesPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fuzzData, err := os.ReadFile(*fuzzTargetsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	standards, err := parseStandards(*standardsPath, standardsData)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	packages, err := parsePackageOwnership(*packagesPath, packagesData)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	platformContracts, err := parsePlatformContracts(*platformsPath, platformsData)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	services, err := parseServiceContracts(*servicesPath, servicesData)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fuzz, err := parseFuzzTargets(*fuzzTargetsPath, fuzzData)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	manifests := Manifests{Standards: standards, Packages: packages, Platforms: platformContracts, Services: services, Fuzz: fuzz}

	switch {
	case *printShard != "":
		return runPrintShard(manifests, *printShard, stdout, stderr)
	case *printPlatformPackages != "":
		return runPrintPlatformPackages(manifests, *printPlatformPackages, stdout, stderr)
	case *printServiceRun != "":
		return runPrintServiceRun(manifests, *printServiceRun, stdout, stderr)
	case *printFuzzTargetsJSON != "":
		return runPrintFuzzTargetsJSON(manifests, *printFuzzTargetsJSON, stdout, stderr)
	}

	problems := validateStatic(manifests)
	if len(problems) == 0 {
		facts, factErr := collectRepositoryFacts(context.Background(), manifests, *workflowPath, *makefilePath)
		if factErr != nil {
			fmt.Fprintln(stderr, factErr)
			return 2
		}
		problems = validateRepository(manifests, facts)
	}
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintf(stderr, "%s: %s: %s\n", problem.Code, problem.Path, problem.Message)
		}
		return 1
	}
	fmt.Fprintf(stdout, "civalidate: OK (%d standards, %d packages, %d platform contracts, %d services)\n", len(standards.Standards), len(packages.Packages), len(platformContracts.Contracts), len(services.Services))
	return 0
}

func goTestPath(path string) string {
	if path == "." {
		return "."
	}
	return "./" + path
}

func runPrintShard(m Manifests, id string, stdout, stderr io.Writer) int {
	shards := m.Packages.LinuxRaceShards
	if shards == nil {
		fmt.Fprintln(stderr, "linux_race_shards is not declared")
		return 1
	}
	for _, shard := range shards.Shards {
		if shard.ID != id {
			continue
		}
		if len(shard.Packages) == 0 {
			fmt.Fprintf(stderr, "shard %s has no packages\n", id)
			return 1
		}
		paths := make([]string, 0, len(shard.Packages))
		for _, pkg := range shard.Packages {
			paths = append(paths, goTestPath(pkg))
		}
		fmt.Fprintln(stdout, strings.Join(paths, " "))
		return 0
	}
	fmt.Fprintf(stderr, "shard %s not found\n", id)
	return 1
}

func runPrintPlatformPackages(m Manifests, goos string, stdout, stderr io.Writer) int {
	seen := map[string]struct{}{}
	var paths []string
	for _, contract := range m.Platforms.Contracts {
		if contract.GOOS != goos {
			continue
		}
		if _, exists := seen[contract.Package]; exists {
			continue
		}
		seen[contract.Package] = struct{}{}
		paths = append(paths, goTestPath(contract.Package))
	}
	if len(paths) == 0 {
		fmt.Fprintf(stderr, "no focused contracts for GOOS %s\n", goos)
		return 1
	}
	fmt.Fprintln(stdout, strings.Join(paths, " "))
	return 0
}

func runPrintServiceRun(m Manifests, id string, stdout, stderr io.Writer) int {
	for _, service := range m.Services.Services {
		if service.ID != id {
			continue
		}
		if len(service.SelectedTests) == 0 || len(service.Packages) == 0 {
			fmt.Fprintf(stderr, "service %s has an empty selector\n", id)
			return 1
		}
		paths := make([]string, 0, len(service.Packages))
		for _, pkg := range service.Packages {
			paths = append(paths, goTestPath(pkg))
		}
		parts := []string{"-run", "^(" + strings.Join(service.SelectedTests, "|") + ")$"}
		if service.Parallel > 0 {
			parts = append(parts, "-p", strconv.Itoa(service.Parallel))
		}
		parts = append(parts, "-count=1")
		parts = append(parts, paths...)
		fmt.Fprintln(stdout, strings.Join(parts, " "))
		return 0
	}
	fmt.Fprintf(stderr, "service %s not found\n", id)
	return 1
}

type fuzzTargetJSON struct {
	Package string `json:"package"`
	Target  string `json:"target"`
	Budget  string `json:"budget"`
}

func runPrintFuzzTargetsJSON(m Manifests, tier string, stdout, stderr io.Writer) int {
	if tier != "all" {
		if _, exists := fuzzTiers[tier]; !exists {
			fmt.Fprintf(stderr, "unknown fuzz tier %q (want pr, scheduled, or all)\n", tier)
			return 1
		}
	}
	out := []fuzzTargetJSON{}
	for _, target := range m.Fuzz.Targets {
		if tier != "all" && target.Tier != tier {
			continue
		}
		out = append(out, fuzzTargetJSON{
			Package: target.Package,
			Target:  target.Target,
			Budget:  strconv.Itoa(target.BudgetSeconds) + "s",
		})
	}
	if len(out) == 0 {
		fmt.Fprintf(stderr, "no fuzz targets for tier %s\n", tier)
		return 1
	}
	data, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, string(data))
	return 0
}

var testNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func discoverServiceTests(ctx context.Context, m Manifests) (map[string][]string, error) {
	out := map[string][]string{}
	for _, service := range m.Services.Services {
		if len(service.SelectedTests) == 0 || len(service.Packages) == 0 {
			continue
		}
		selector := "^(" + strings.Join(service.SelectedTests, "|") + ")$"
		found := map[string]struct{}{}
		for _, pkg := range service.Packages {
			cmd := exec.CommandContext(ctx, "go", "test", "-list", selector, goTestPath(pkg))
			data, err := cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("service %s: test discovery for %s: %w", service.ID, pkg, err)
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if testNamePattern.MatchString(line) {
					found[line] = struct{}{}
				}
			}
		}
		names := make([]string, 0, len(found))
		for name := range found {
			names = append(names, name)
		}
		sort.Strings(names)
		out[service.ID] = names
	}
	return out, nil
}

func collectRepositoryFacts(ctx context.Context, m Manifests, workflowPath, makefilePath string) (RepositoryFacts, error) {
	packages, err := listGoPackages(ctx, ".", []string{"linux", "darwin", "windows"})
	if err != nil {
		return RepositoryFacts{}, err
	}
	workflowData, err := os.ReadFile(workflowPath)
	if err != nil {
		return RepositoryFacts{}, err
	}
	workflow, err := parseWorkflow(workflowPath, workflowData)
	if err != nil {
		return RepositoryFacts{}, err
	}
	makeData, err := os.ReadFile(makefilePath)
	if err != nil {
		return RepositoryFacts{}, err
	}
	makeTargets, err := parseMakeTargets(makeData)
	if err != nil {
		return RepositoryFacts{}, err
	}
	determinismData, err := os.ReadFile("scripts/check-determinism.sh")
	if err != nil {
		return RepositoryFacts{}, err
	}
	determinismPackages, err := parseDeterminismPackages(determinismData)
	if err != nil {
		return RepositoryFacts{}, err
	}
	signals, scanErr := scanPlatformSignals(os.DirFS("."))
	fuzzTargets, fuzzErr := scanFuzzTargets(os.DirFS("."))
	if fuzzErr != nil {
		return RepositoryFacts{}, fmt.Errorf("fuzz target scan: %w", fuzzErr)
	}
	serviceTests, err := discoverServiceTests(ctx, m)
	if err != nil {
		return RepositoryFacts{}, err
	}
	expectedWorkflows := map[string]struct{}{}
	if entries, dirErr := os.ReadDir(".github/workflows"); dirErr == nil {
		for _, entry := range entries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(".github/workflows", entry.Name()))
			if readErr != nil {
				continue
			}
			if wf, parseErr := parseWorkflow(entry.Name(), data); parseErr == nil && wf.Name != "" {
				expectedWorkflows[wf.Name] = struct{}{}
			} else if name := workflowNameOnly(data); name != "" {
				expectedWorkflows[name] = struct{}{}
			}
		}
	}
	facts := RepositoryFacts{
		GoPackages:          packages,
		Workflow:            workflow,
		WorkflowRaw:         string(workflowData),
		MakeTargets:         makeTargets,
		DeterminismPackages: determinismPackages,
		PlatformSignals:     signals,
		ExistingPaths:       map[string]struct{}{},
		ExpectedWorkflows:   expectedWorkflows,
		ServiceTests:        serviceTests,
		FuzzTargets:         fuzzTargets,
	}
	if scanErr != nil {
		facts.PlatformParseErrors = []string{scanErr.Error()}
	}
	for _, item := range m.Platforms.Inventory {
		if _, err := os.Stat(item.Path); err == nil {
			facts.ExistingPaths[item.Path] = struct{}{}
		}
	}
	for _, standard := range m.Standards.Standards {
		for _, witness := range standard.Witnesses {
			for _, path := range witness.Coverage.GeneratedPaths {
				if _, err := os.Stat(path); err == nil {
					facts.ExistingPaths[path] = struct{}{}
				}
			}
		}
	}
	return facts, nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func mappingRoot(node *yaml.Node) (*yaml.Node, error) {
	if node.Kind != yaml.DocumentNode || len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("expected one mapping document")
	}
	return node.Content[0], nil
}

// workflowNameOnly extracts the top-level `name:` from a workflow document
// without requiring a `jobs:` mapping. It is used only to build the
// known-workflow-name set, where a parseable-but-jobs-less file must still
// contribute its name instead of silently dropping out (which would turn a
// legitimate cross-workflow reference into a false workflow-mismatch).
func workflowNameOnly(data []byte) string {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return ""
	}
	root, err := mappingRoot(&node)
	if err != nil {
		return ""
	}
	if value := mappingValue(root, "name"); value != nil {
		return value.Value
	}
	return ""
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".sage", ".sage-memory", "node_modules", "vendor", "dist", "build", "testdata":
		return true
	default:
		return false
	}
}

func require(problems *Problems, path, value string) {
	if strings.TrimSpace(value) == "" {
		problems.add(ProblemMissingField, path, "required field is empty")
	}
}

func checkEnum(problems *Problems, path, value string, allowed map[string]struct{}) {
	if value == "" {
		problems.add(ProblemMissingField, path, "required enum is empty")
		return
	}
	if _, exists := allowed[value]; !exists {
		problems.add(ProblemUnknownEnum, path, "unknown value "+value)
	}
}

func (p *Problems) add(code ProblemCode, path, message string) {
	*p = append(*p, Problem{Code: code, Path: path, Message: message})
}

func (p Problems) sort() {
	sort.Slice(p, func(i, j int) bool {
		if p[i].Path != p[j].Path {
			return p[i].Path < p[j].Path
		}
		if p[i].Code != p[j].Code {
			return p[i].Code < p[j].Code
		}
		return p[i].Message < p[j].Message
	})
}

func setOf(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func sliceSet(values []string) map[string]struct{} {
	return setOf(values...)
}
