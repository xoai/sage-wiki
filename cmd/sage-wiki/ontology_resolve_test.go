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
	c.Flags().String("unlink", "", "")
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

// GATE-3 R4. The exported guard had NO test at all, and it is the reason the
// check was exported: without it a human completes, via --apply, a merge the
// compile pass refused.
func TestResolveCLIApplyRefusesCoAbsorptionThroughAChain(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-row", "x-row", "y-row", "z-row"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// x -> y -> z applied; rows are never rewritten to the terminal.
	for _, p := range [][2]string{{"x-row", "y-row"}, {"y-row", "z-row"}} {
		if err := ont.PutAlias(store.EntityAlias{
			Alias: p[0], CanonicalID: p[1], EntityType: ontology.TypeConcept,
			Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The user separated a-row from x-row, which now sits two hops under z-row.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "a-row", CanonicalID: "x-row", EntityType: ontology.TypeConcept,
		Status: store.AliasRejected, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		DecidedBy: "user",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "a-row", CanonicalID: "z-row", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Confidence: 0.95, Source: "llm",
		CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	err = runResolve(t, map[string]string{"apply": "a-row"})
	if err == nil {
		t.Fatal("--apply completed a merge the pass refuses: a-row joins x-row " +
			"transitively under z-row despite the user separating them")
	}
	if !strings.Contains(err.Error(), "x-row") {
		t.Errorf("the error should name the conflicting entity, got: %v", err)
	}

	// Nothing was written.
	b2, ont2, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	act, _ := ont2.GetActiveAlias("a-row")
	if act == nil || act.Status != store.AliasPending {
		t.Errorf("the pending row should be left in place, got %+v", act)
	}
}

// GATE-3 R4. Applying a stale pending row after the reverse link was applied
// would create a 2-cycle in which neither entity is canonical.
func TestResolveCLIApplyRefusesCycle(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// B -> A already applied; a stale pending A -> B remains.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "B", CanonicalID: "A", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "B", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Confidence: 0.95, Source: "llm",
		CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	if err := runResolve(t, map[string]string{"apply": "A"}); err == nil {
		t.Fatal("--apply created a cycle: B already resolves to A, so linking A -> B " +
			"leaves neither entity canonical")
	}
}

// GATE-3 R5. "will not be proposed again, in either direction" must be true of
// the CURRENT state too: a pending row can coexist with an applied row for the
// same pair pointing the other way, and rejecting only the pending half leaves
// the applied link live with the sweep copying across it forever.
func TestResolveCLIRejectAlsoClearsTheReverseAppliedLink(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Pending A -> B awaiting a human; applied B -> A already in force.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "B", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Confidence: 0.9, Source: "llm",
		CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "B", CanonicalID: "A", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	if err := runResolve(t, map[string]string{"reject": "A"}); err != nil {
		t.Fatalf("--reject: %v", err)
	}

	b2, ont2, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	applied, _ := ont2.ListAliases(store.AliasApplied)
	for _, r := range applied {
		if r.Alias == "B" && r.CanonicalID == "A" {
			t.Error("the reverse APPLIED link survived a rejection that promised " +
				"the pair would not be linked in either direction")
		}
	}
}

// GATE-3 R6. --reject must clear the reverse row whether it is applied OR
// pending. Clearing only the applied case leaves an unapplicable pending row:
// --review lists it forever, --apply always errors (the pair is now rejected),
// and its active status freezes the alias out of resolution — leaving --reject
// on the other half, a judgement the user never made, as the only escape.
func TestResolveCLIRejectAlsoClearsAPendingReverseRow(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Applied A -> B, and a PENDING B -> A still queued the other way.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "B", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "B", CanonicalID: "A", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Confidence: 0.9, Source: "llm",
		CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	if err := runResolve(t, map[string]string{"reject": "A"}); err != nil {
		t.Fatalf("--reject: %v", err)
	}

	b2, ont2, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()

	pending, _ := ont2.ListAliases(store.AliasPending)
	for _, r := range pending {
		if r.Alias == "B" && r.CanonicalID == "A" {
			t.Error("a PENDING reverse row survived the rejection: it can never be " +
				"applied, and its active status freezes B out of resolution")
		}
	}
	if act, _ := ont2.GetActiveAlias("B"); act != nil {
		t.Errorf("B still carries an active row after the pair was rejected: %+v", act)
	}
}

// GATE-3 R7. --reject must not over-reach: it clears only the row pointing
// directly back, never an unrelated link further along a chain.
func TestResolveCLIRejectDoesNotOverReach(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B", "C"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// A -> B pending; B -> C applied (a DIFFERENT pair further along the chain).
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "B", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Confidence: 0.9, Source: "llm",
		CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "B", CanonicalID: "C", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	if err := runResolve(t, map[string]string{"reject": "A"}); err != nil {
		t.Fatalf("--reject: %v", err)
	}

	b2, ont2, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	act, _ := ont2.GetActiveAlias("B")
	if act == nil || act.CanonicalID != "C" || act.Status != store.AliasApplied {
		t.Errorf("rejecting A -> B also disturbed the unrelated B -> C link: %+v", act)
	}
}

// GATE-3 R7. The CLI --sweep must run the SHARED implementation, including its
// rejection filter — a CLI-local copy is how the filter came to exist on one
// path and not the other.
func TestResolveCLISweepHonoursRejections(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alias", "canon", "tgt"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "canon", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// The user rejects the pair the other way round.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "canon", CanonicalID: "alias", EntityType: ontology.TypeConcept,
		Status: store.AliasRejected, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		DecidedBy: "user",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ont.AddRelation(store.Relation{
		ID: "late", SourceID: "alias", TargetID: "tgt",
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
	canon, _ := ont2.GetRelations("canon", store.Outbound, "")
	if len(canon) != 0 {
		t.Errorf("--sweep copied across a rejected pair: %+v", canon)
	}
}

// GATE-3 R7. The "edges remain" note must name the entity the copies actually
// landed on. When the APPLIED half is the reverse row, that is the reverse row's
// canonical — pointing the user elsewhere is worse than silence, because
// --unlink takes an alias id and following the wrong name would undo the wrong
// link.
func TestResolveCLIRejectNamesTheEntityHoldingTheResidue(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Applied A -> B (so the copies live on B); pending B -> A the other way.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "B", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "B", CanonicalID: "A", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Confidence: 0.9, Source: "llm",
		CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := runResolve(t, map[string]string{"reject": "B"}) // reject from the pending side
	w.Close()
	os.Stdout = orig
	if runErr != nil {
		t.Fatalf("--reject: %v", runErr)
	}
	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	r.Close()
	out := string(buf[:n])

	if !strings.Contains(out, "Edges copied onto B remain") {
		t.Errorf("the residue note names the wrong entity — the applied link was "+
			"A -> B, so the copies are on B:\n%s", out)
	}
}

// GATE-3 R8. The residue fact must reach the machine-readable path too: a
// scripted consumer told only "rejected" has no way to learn that the canonical
// still holds derived edges, or that --unlink is what removes them.
func TestResolveCLIRejectJSONCarriesResidue(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "B", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := runResolve(t, map[string]string{"reject": "A"})
	w.Close()
	os.Stdout = orig
	if runErr != nil {
		t.Fatalf("--reject: %v", runErr)
	}
	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	r.Close()
	out := string(buf[:n])

	if !json.Valid([]byte(out)) {
		t.Fatalf("--reject --format json is not valid JSON:\n%s", out)
	}
	if !strings.Contains(out, "edges_remain_on") || !strings.Contains(out, `"B"`) {
		t.Errorf("the JSON payload does not say which entity keeps the copied edges:\n%s", out)
	}
}

// --unlink had no test at the layer users touch: ~45 lines of command — the
// flag, both guards, UnlinkAlias, the re-sweep and two output shapes — and the
// helper above did not even register the flag. UnlinkConformance covers the
// STORE method on both backends; this covers the command.
func TestResolveCLIUnlinkRemovesDerivedEdgesAndRejectsThePair(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	seedResolveStore(t, dir, "config.yaml")
	withProject(t, dir, "config.yaml")

	// Promote the seeded proposal to an applied link.
	if err := runResolve(t, map[string]string{"apply": "Buzz Aldrin"}); err != nil {
		t.Fatalf("--apply: %v", err)
	}

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := ont.GetRelations("buzz-aldrin", store.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	if len(linked) != 1 {
		t.Fatalf("setup: the canonical should see the alias's edge, got %d", len(linked))
	}

	if err := runResolve(t, map[string]string{"unlink": "Buzz Aldrin"}); err != nil {
		t.Fatalf("--unlink: %v", err)
	}

	b, ont, err = openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	after, err := ont.GetRelations("buzz-aldrin", store.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("canonical still sees %d edges after --unlink: %+v", len(after), after)
	}
	// The alias keeps its own edge — unlink separates, it does not delete.
	own, err := ont.GetRelations("Buzz Aldrin", store.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 {
		t.Errorf("the alias lost its own edge: %+v", own)
	}
	// And the pair must be rejected, or the next compile re-applies it.
	rejected, err := ont.IsRejected("Buzz Aldrin", "buzz-aldrin")
	if err != nil {
		t.Fatal(err)
	}
	if !rejected {
		t.Error("--unlink left the pair un-rejected; the next compile would re-apply it")
	}
}

// Both guards: --unlink on an unknown alias, and on one that is merely pending.
func TestResolveCLIUnlinkGuards(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	seedResolveStore(t, dir, "config.yaml")
	withProject(t, dir, "config.yaml")

	if err := runResolve(t, map[string]string{"unlink": "no-such-alias"}); err == nil {
		t.Error("--unlink on an unknown alias should error")
	}
	// The seeded row is PENDING, not applied — --reject decides those.
	if err := runResolve(t, map[string]string{"unlink": "Buzz Aldrin"}); err == nil {
		t.Error("--unlink on a pending proposal should error and point at --reject")
	}
}

// The re-sweep inside resolveUnlink is what makes undo correct for TRANSITIVE
// chains — under A→B→C, edges that reached C are stamped with the intermediate
// alias B, so delete-by-cause on A cannot touch them. The single-link test
// above passes without the re-sweep; this one does not.
func TestResolveCLIUnlinkClearsTransitiveChain(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"aa", "bb", "cc", "edge"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ont.AddRelation(store.Relation{
		ID: "r1", SourceID: "aa", TargetID: "edge", Relation: ontology.RelExtends,
	}); err != nil {
		t.Fatal(err)
	}
	mk := func(alias, canon string) store.EntityAlias {
		return store.EntityAlias{
			Alias: alias, CanonicalID: canon, EntityType: ontology.TypeConcept,
			Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
		}
	}
	if _, err := ont.LinkAlias(mk("aa", "bb")); err != nil {
		t.Fatal(err)
	}
	if _, err := ont.LinkAlias(mk("bb", "cc")); err != nil {
		t.Fatal(err)
	}
	chained, err := ont.GetRelations("cc", store.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	if len(chained) != 1 {
		t.Fatalf("setup: cc should see aa's edge through the chain, got %d", len(chained))
	}

	if err := runResolve(t, map[string]string{"unlink": "aa"}); err != nil {
		t.Fatalf("--unlink: %v", err)
	}

	b, ont, err = openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ccRels, err := ont.GetRelations("cc", store.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ccRels) != 0 {
		t.Errorf("cc still sees %d edges after `--unlink aa` — the transitively derived "+
			"row (stamped bb) survived, so resolveUnlink's re-sweep is not running: %+v",
			len(ccRels), ccRels)
	}
	// aa keeps its own edge: unlink separates, it does not delete.
	own, err := ont.GetRelations("aa", store.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 {
		t.Errorf("aa lost its own edge: %+v", own)
	}
}
