package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// resolveVault builds a project dir with a config and a seeded ontology,
// returning the dir. It writes config.yaml under a caller-chosen name so the
// --config flag can be exercised.
func resolveVault(t *testing.T, configName string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".sage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configName), []byte("project: resolve-test\noutput: wiki\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func seedResolveStore(t *testing.T, dir string, configName string) {
	t.Helper()
	old := configPath
	configPath = configName
	defer func() { configPath = old }()

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatalf("openResolveStore: %v", err)
	}
	defer b.Close()

	for _, e := range []store.Entity{
		{ID: "buzz-aldrin", Type: ontology.TypeConcept, Name: "Buzz Aldrin", ArticlePath: "wiki/buzz.md"},
		{ID: "Buzz Aldrin", Type: ontology.TypeConcept, Name: "Buzz Aldrin", Definition: "Apollo 11 pilot"},
		{ID: "apollo-11", Type: ontology.TypeConcept, Name: "Apollo 11"},
	} {
		if err := ont.AddEntity(e); err != nil {
			t.Fatalf("AddEntity %s: %v", e.ID, err)
		}
	}
	if err := ont.AddRelation(store.Relation{
		ID: "r1", SourceID: "Buzz Aldrin", TargetID: "apollo-11",
		Relation: ontology.RelExtends, Confidence: 0.8,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "Buzz Aldrin", CanonicalID: "buzz-aldrin", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Confidence: 0.9, Reason: "same astronaut",
		Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
}

func withProject(t *testing.T, dir, configName string) {
	t.Helper()
	oldDir, oldCfg := projectDir, configPath
	projectDir, configPath = dir, configName
	t.Cleanup(func() { projectDir, configPath = oldDir, oldCfg })
}

func runResolve(t *testing.T, flags map[string]string) error {
	t.Helper()
	cmd := *ontologyResolveCmd
	c := &cmd
	c.ResetFlags()
	c.Flags().Bool("review", false, "")
	c.Flags().String("apply", "", "")
	c.Flags().String("reject", "", "")
	c.Flags().Bool("sweep", false, "")
	for k, v := range flags {
		if err := c.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s: %v", k, err)
		}
	}
	return runOntologyResolve(c, nil)
}

// Exactly one action flag. Zero is nearly always a mistyped flag; two would be
// ambiguous about ordering.
func TestResolveCLIRequiresExactlyOneFlag(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	if err := runResolve(t, nil); err == nil {
		t.Error("no flags: want an error")
	}
	if err := runResolve(t, map[string]string{"review": "true", "sweep": "true"}); err == nil {
		t.Error("two flags: want an error")
	}
}

func TestResolveCLIReviewListsPending(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	seedResolveStore(t, dir, "config.yaml")
	withProject(t, dir, "config.yaml")

	if err := runResolve(t, map[string]string{"review": "true"}); err != nil {
		t.Fatalf("--review: %v", err)
	}
}

// --apply performs a real write: the canonical must gain the alias's edge and
// BOTH entity rows must survive.
func TestResolveCLIApplyLinks(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	seedResolveStore(t, dir, "config.yaml")
	withProject(t, dir, "config.yaml")

	if err := runResolve(t, map[string]string{"apply": "Buzz Aldrin"}); err != nil {
		t.Fatalf("--apply: %v", err)
	}

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	act, err := ont.GetActiveAlias("Buzz Aldrin")
	if err != nil || act == nil || act.Status != store.AliasApplied {
		t.Fatalf("alias not applied: %+v %v", act, err)
	}
	if act.DecidedBy != "user" {
		t.Errorf("DecidedBy = %q, want user", act.DecidedBy)
	}
	canon, err := ont.GetRelations("buzz-aldrin", store.Outbound, "")
	if err != nil || len(canon) != 1 || canon[0].TargetID != "apollo-11" {
		t.Errorf("canonical did not gain the edge: %+v %v", canon, err)
	}
	for _, id := range []string{"buzz-aldrin", "Buzz Aldrin"} {
		if e, _ := ont.GetEntity(id); e == nil {
			t.Errorf("entity %q deleted by --apply", id)
		}
	}
}

// A rejection blocks the pair in BOTH directions.
func TestResolveCLIRejectIsSymmetric(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	seedResolveStore(t, dir, "config.yaml")
	withProject(t, dir, "config.yaml")

	if err := runResolve(t, map[string]string{"reject": "Buzz Aldrin"}); err != nil {
		t.Fatalf("--reject: %v", err)
	}

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if act, _ := ont.GetActiveAlias("Buzz Aldrin"); act != nil {
		t.Errorf("row still active after rejection: %+v", act)
	}
	for _, pair := range [][2]string{{"Buzz Aldrin", "buzz-aldrin"}, {"buzz-aldrin", "Buzz Aldrin"}} {
		rejected, err := ont.IsRejected(pair[0], pair[1])
		if err != nil || !rejected {
			t.Errorf("IsRejected(%s,%s) = %v, want true", pair[0], pair[1], rejected)
		}
	}
}

// --sweep copies edges that landed on an alias after the link was applied, with
// no LLM involvement. This is the remedy for the compile-path coverage gap.
func TestResolveCLISweepCopiesLateEdges(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	seedResolveStore(t, dir, "config.yaml")
	withProject(t, dir, "config.yaml")

	if err := runResolve(t, map[string]string{"apply": "Buzz Aldrin"}); err != nil {
		t.Fatalf("--apply: %v", err)
	}

	// A new edge appears on the alias afterwards.
	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ont.AddEntity(store.Entity{ID: "gemini-12", Type: ontology.TypeConcept, Name: "Gemini 12"}); err != nil {
		t.Fatal(err)
	}
	if err := ont.AddRelation(store.Relation{
		ID: "late", SourceID: "Buzz Aldrin", TargetID: "gemini-12",
		Relation: ontology.RelExtends, Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	if err := runResolve(t, map[string]string{"sweep": "true"}); err != nil {
		t.Fatalf("--sweep: %v", err)
	}

	b2, ont2, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	canon, err := ont2.GetRelations("buzz-aldrin", store.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]bool{}
	for _, r := range canon {
		targets[r.TargetID] = true
	}
	if !targets["gemini-12"] {
		t.Errorf("sweep did not copy the late edge; canonical targets = %v", targets)
	}
}

// The global --config flag must be honoured, or this subcommand reads a
// different database from its siblings under the same flag.
func TestResolveCLIHonoursConfigFlag(t *testing.T) {
	dir := resolveVault(t, "pg.yaml")
	seedResolveStore(t, dir, "pg.yaml")
	withProject(t, dir, "pg.yaml")

	if err := runResolve(t, map[string]string{"review": "true"}); err != nil {
		t.Fatalf("--review with --config pg.yaml: %v", err)
	}

	// With the flag cleared, config.yaml does not exist, so the open must fail
	// rather than silently reading a default vault.
	configPath = ""
	err := runResolve(t, map[string]string{"review": "true"})
	if err == nil {
		t.Error("expected an error when the named config is absent")
	}
}

// Guard the seam choice: the subcommand must hold a store.Backend, not the
// concrete sqlite *storage.DB that OpenConcrete unwraps to — that path returns
// "unexpected backend type" under backend: postgres.
func TestResolveCLIUsesBackendAgnosticSeam(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatalf("openResolveStore: %v", err)
	}
	defer b.Close()

	// openResolveStore's signature already guarantees the backend-agnostic
	// types; what needs asserting is that the handle is WRITABLE.
	// Writer mode is mandatory: --apply and --sweep write, and a reader handle
	// fails every write path.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "x", CanonicalID: "y", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Errorf("store opened read-only — --apply and --sweep would fail: %v", err)
	}
}

// --format json must emit parseable snake_case on EVERY resolve path — the json
// tags on EntityAlias/LinkResult are otherwise unverified.
func TestResolveCLIJSONOutput(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	seedResolveStore(t, dir, "config.yaml")
	withProject(t, dir, "config.yaml")

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	capture := func(t *testing.T, fn func() error) string {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		orig := os.Stdout
		os.Stdout = w
		runErr := fn()
		w.Close()
		os.Stdout = orig
		if runErr != nil {
			t.Fatalf("command failed: %v", runErr)
		}
		buf := make([]byte, 8192)
		n, _ := r.Read(buf)
		r.Close()
		return string(buf[:n])
	}

	// --review emits the alias payload with snake_case keys.
	out := capture(t, func() error { return runResolve(t, map[string]string{"review": "true"}) })
	if !json.Valid([]byte(out)) {
		t.Fatalf("--review --format json is not valid JSON:\n%s", out)
	}
	for _, key := range []string{"canonical_id", "entity_type", "created_at"} {
		if !strings.Contains(out, key) {
			t.Errorf("--review json missing snake_case key %q:\n%s", key, out)
		}
	}
	if strings.Contains(out, "CanonicalID") {
		t.Errorf("--review json emitted Go field names instead of json tags:\n%s", out)
	}

	// --apply emits the LinkResult payload.
	out = capture(t, func() error { return runResolve(t, map[string]string{"apply": "Buzz Aldrin"}) })
	if !json.Valid([]byte(out)) {
		t.Fatalf("--apply --format json is not valid JSON:\n%s", out)
	}
	for _, key := range []string{"copied", "skipped", "self_loops"} {
		if !strings.Contains(out, key) {
			t.Errorf("--apply json missing key %q:\n%s", key, out)
		}
	}

	// --sweep emits counts.
	out = capture(t, func() error { return runResolve(t, map[string]string{"sweep": "true"}) })
	if !json.Valid([]byte(out)) {
		t.Fatalf("--sweep --format json is not valid JSON:\n%s", out)
	}
}
