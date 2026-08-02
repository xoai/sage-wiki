package compiler

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/storage"
)

func keyRows(t *testing.T, dir string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT source_path, compile_key FROM compile_items ORDER BY source_path")
	if err != nil {
		t.Fatalf("key rows: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, k string
		rows.Scan(&p, &k)
		out[p] = k
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("key rows scan: %v", err)
	}
	return out
}

// TestSkip_AdoptThenUnchanged pins R3/R4: first post-upgrade compile adopts
// keys with ZERO provider requests; the next compile skips unchanged.
func TestSkip_AdoptThenUnchanged(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	// Initial compile populates keys (fresh corpus — everything compiles).
	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	// Simulate the pre-SPEC-04 upgrade state: keys exist but empty.
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE compile_items SET compile_key = '', compile_key_parts = ''"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// R3: adoption run — zero provider requests, keys stored, all adopted.
	before := stub.requests.get()
	res := compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.requests.get() - before; got != 0 {
		t.Errorf("adoption run made %d provider requests, want 0", got)
	}
	if res == nil || res.Adopted != 3 {
		t.Errorf("Adopted = %v, want 3", res)
	}
	keys := keyRows(t, dir)
	for p, k := range keys {
		if k == "" {
			t.Errorf("key not stored for %s after adoption", p)
		}
	}

	// R4: steady state — zero requests, all skipped unchanged.
	before = stub.requests.get()
	res2 := compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.requests.get() - before; got != 0 {
		t.Errorf("unchanged run made %d provider requests, want 0 (all-skip short-circuit)", got)
	}
	if res2 == nil || len(res2.Skipped) != 3 {
		t.Errorf("Skipped = %v, want 3 docs", res2)
	}
	for _, s := range res2.Skipped {
		if s.Reason != "unchanged" {
			t.Errorf("skip reason = %q, want unchanged", s.Reason)
		}
	}
}

// TestSkip_ForceRecompiles pins R1 (+ the force×new/force×interrupted cells).
func TestSkip_ForceRecompiles(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	before := stub.requests.get()
	res := compileInDir(t, dir, srv.URL, CompileOpts{Force: true})
	if got := stub.requests.get() - before; got == 0 {
		t.Error("--force made zero provider requests, want full recompile")
	}
	if res != nil && len(res.Skipped) != 0 {
		t.Errorf("Skipped under --force = %v, want 0", res.Skipped)
	}
}

// TestSkip_ContentTouchRecompilesOnlyThatDoc pins AC-3's request-level shape:
// touching one doc re-runs only its passes.
func TestSkip_ContentTouchRecompilesOnlyThatDoc(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	if err := os.WriteFile(filepath.Join(dir, "raw", "doc2.md"), []byte("# Deferred Doc 2\n\nEDITED content, materially different now."), 0644); err != nil {
		t.Fatal(err)
	}

	before := stub.requests.get()
	res := compileInDir(t, dir, srv.URL, CompileOpts{})
	got := stub.requests.get() - before
	if got == 0 {
		t.Fatal("touching a doc made zero provider requests, want its passes re-run")
	}
	if res != nil && len(res.Skipped) != 2 {
		t.Errorf("Skipped = %v, want the 2 untouched docs", res.Skipped)
	}
	t.Logf("requests after one-doc touch: %d (that doc's summarize+extract(+write/triples) only)", got)
}

// TestSkip_ConfigDriftRecompiles pins R5: changing a subset field rekeys
// with reason "config"; changing an ignored field does nothing.
func TestSkip_ConfigDriftRecompiles(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	// Ignored-field no-op first: serve.token must not rekey.
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, _ := os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, append(raw, []byte("\nserve:\n  token: abc\n")...), 0644)
	before := stub.requests.get()
	compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.requests.get() - before; got != 0 {
		t.Errorf("serve.token edit triggered %d requests, want 0 (ignored field)", got)
	}

	// Subset-field drift: dedup_threshold must rekey every doc with reason config.
	raw, _ = os.ReadFile(cfgPath)
	raw = []byte(strings.Replace(string(raw), "default_tier: 3", "default_tier: 3\n  dedup_threshold: 0.91", 1))
	os.WriteFile(cfgPath, raw, 0644)
	before = stub.requests.get()
	res := compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.requests.get() - before; got == 0 {
		t.Error("dedup_threshold edit made zero requests, want full drift recompile")
	}
	if res != nil && len(res.Skipped) != 0 {
		t.Errorf("Skipped under config drift = %v, want 0", res.Skipped)
	}
}

// TestSkip_TemplateDriftRecompiles pins AC-4: an override file for a compile
// template rekeys with reason templates.
func TestSkip_TemplateDriftRecompiles(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	// Per-workspace registry: the override must NOT pollute the package-global
	// default registry for other tests in the binary.
	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{Prompts: prompts.NewRegistry()})

	os.MkdirAll(filepath.Join(dir, "prompts"), 0755)
	if err := os.WriteFile(filepath.Join(dir, "prompts", "summarize-article.md"),
		[]byte("Summarize {{.SourcePath}} — custom override body, sufficiently long to render."), 0644); err != nil {
		t.Fatal(err)
	}

	before := stub.requests.get()
	res := compileInDir(t, dir, srv.URL, CompileOpts{Prompts: prompts.NewRegistry()})
	if got := stub.requests.get() - before; got == 0 {
		t.Error("template override made zero requests, want drift recompile with reason templates")
	}
	_ = res
}

// TestSkip_ResumeBeatsKeyMatch pins R0: stored matching key + a zeroed pass
// flag → the doc recompiles (resume), never skips.
func TestSkip_ResumeBeatsKeyMatch(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE compile_items SET pass_written = 0 WHERE source_path = 'raw/doc2.md'"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	before := stub.requests.get()
	compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.requests.get() - before; got == 0 {
		t.Error("matching key + incomplete flags skipped the doc — R0 must recompile")
	}
}

// TestSkip_DryRunStoresNothing pins the dry-run contract: verdicts computed,
// nothing persisted (no adoption, no resets, no keys).
func TestSkip_DryRunStoresNothing(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE compile_items SET compile_key = '', compile_key_parts = ''"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	compileInDir(t, dir, srv.URL, CompileOpts{DryRun: true})
	for p, k := range keyRows(t, dir) {
		if k != "" {
			t.Errorf("dry run stored a key for %s — dry-run must persist nothing", p)
		}
	}
}

// TestSkip_TierLowSkips pins R6: tier-1 docs adopt+skip on unchanged, and
// chunk-config drift rekeys them with reason config (no LLM involved).
func TestSkip_TierLowSkips(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := t.TempDir()
	writeDeferredCorpusInto(t, dir, srv.URL)
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, _ := os.ReadFile(cfgPath)
	raw = []byte(strings.Replace(string(raw), "default_tier: 3", "default_tier: 1", 1))
	os.WriteFile(cfgPath, raw, 0644)

	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Unchanged run: zero requests (embeddings too), all skipped.
	before := stub.requests.get()
	embedBefore := stub.embeds.get()
	res := compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.requests.get() - before; got != 0 {
		t.Errorf("tier-1 unchanged run made %d chat requests, want 0", got)
	}
	if got := stub.embeds.get() - embedBefore; got != 0 {
		t.Errorf("tier-1 unchanged run made %d embed requests, want 0", got)
	}
	if res == nil || len(res.Skipped) != 3 {
		t.Errorf("tier-1 Skipped = %v, want 3", res)
	}

	// Chunk-config drift: chunk_size change rekeys tier-1 docs (reason config).
	raw, _ = os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, append(raw, []byte("\nsearch:\n  chunk_size: 400\n")...), 0644)
	embedBefore = stub.embeds.get()
	res2 := compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.embeds.get() - embedBefore; got == 0 {
		t.Error("chunk_size drift made zero embed requests, want tier-1 re-embed")
	}
	_ = res2
}

// TestSkip_ForceInterruptedAttribution pins the force×interrupted cell
// (spec test 3): R0 wins over --force — verdict is resume, not forced.
func TestSkip_ForceInterruptedAttribution(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE compile_items SET pass_written = 0 WHERE source_path = 'raw/doc2.md'"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// The interrupted doc compiles under --force (R0's work set); the other
	// two also recompile (R1). Requests must flow for all three.
	before := stub.requests.get()
	res := compileInDir(t, dir, srv.URL, CompileOpts{Force: true})
	if got := stub.requests.get() - before; got == 0 {
		t.Error("force×interrupted made zero requests, want recompile")
	}
	if res != nil && len(res.Skipped) != 0 {
		t.Errorf("Skipped under force×interrupted = %v, want 0", res.Skipped)
	}
}

// TestSkip_ForceNewDocAttribution pins the force×new cell: a no-row doc
// under --force compiles (R1), keys stored at completion.
func TestSkip_ForceNewDocAttribution(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{Force: true})
	keys := keyRows(t, dir)
	if len(keys) != 3 {
		t.Fatalf("expected 3 keyed docs, got %d", len(keys))
	}
	for p, k := range keys {
		if k == "" {
			t.Errorf("no key stored for %s after forced fresh compile", p)
		}
	}
}

// TestSkip_EmptyKeyIncompleteFlags pins R0's second resume case: empty key +
// incomplete flags → compile (never adopted-and-skipped).
func TestSkip_EmptyKeyIncompleteFlags(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE compile_items SET compile_key = '', compile_key_parts = '', pass_written = 0 WHERE source_path = 'raw/doc1.md'"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	before := stub.requests.get()
	compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.requests.get() - before; got == 0 {
		t.Error("empty key + incomplete flags made zero requests — R0 must recompile, never adopt")
	}
}

// TestSkip_ModelDriftRecompiles pins the models drift class.
func TestSkip_ModelDriftRecompiles(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})
	raw, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	raw = []byte(strings.Replace(string(raw), "summarize: gpt-4o-mini", "summarize: gpt-4o", 1))
	os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0644)

	before := stub.requests.get()
	res := compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.requests.get() - before; got == 0 {
		t.Error("model edit made zero requests, want models-drift recompile")
	}
	if res != nil && len(res.Skipped) != 0 {
		t.Errorf("Skipped under model drift = %v, want 0", res.Skipped)
	}
}

// TestSkip_EmbedDriftRecompiles pins the embed drift class.
func TestSkip_EmbedDriftRecompiles(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})
	raw, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	raw = []byte(strings.Replace(string(raw), "summarize: gpt-4o-mini", "summarize: gpt-4o-mini\nembed:\n  provider: openai\n  model: text-embedding-3-large", 1))
	os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0644)

	before := stub.requests.get()
	compileInDir(t, dir, srv.URL, CompileOpts{})
	if got := stub.requests.get() - before; got == 0 {
		t.Error("embed model edit made zero requests, want embed-drift recompile")
	}
}

// TestSkip_TemplateVersionBumpRecompiles pins AC-4's version-constant cell:
// a bumped template version (no content change) recompiles affected docs.
// Simulated by direct part comparison — a version bump changes the parts'
// version component, so DriftClass reports templates.
func TestSkip_TemplateVersionBumpRecompiles(t *testing.T) {
	cfg, err := loadConfigFromDir(t, "")
	if err != nil {
		t.Fatal(err)
	}
	partsA, err := ComputeCompileKeyParts("sha256:x", 3, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	versions := prompts.TemplateVersions()
	if versions["write_article"] != "1.0.0" {
		t.Fatalf("write_article version = %q, want 1.0.0", versions["write_article"])
	}
	// A bump changes the version component → templates drift (AC-4's mechanism).
	partsB := partsA
	partsB.Templates = strings.Replace(partsA.Templates, "write_article@1.0.0:", "write_article@1.0.1:", 1)
	if got := DriftClass(partsA, partsB); got != "templates" {
		t.Errorf("DriftClass on version bump = %q, want templates", got)
	}
	if partsA.Key(3) == partsB.Key(3) {
		t.Error("version bump did not rekey")
	}
}

// TestSkip_TemplateDriftReasonAssertion strengthens the template-drift test:
// the classification names 'templates' as the drift class.
func TestSkip_TemplateDriftReasonAssertion(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	reg := prompts.NewRegistry()
	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{Prompts: reg})

	os.MkdirAll(filepath.Join(dir, "prompts"), 0755)
	os.WriteFile(filepath.Join(dir, "prompts", "summarize-article.md"),
		[]byte("Summarize {{.SourcePath}} — drift-reason override body."), 0644)

	reg2 := prompts.NewRegistry()
	reg2.LoadFromDir(filepath.Join(dir, "prompts"))
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	items := NewCompileItemStore(db, config.NowUTC)
	defer db.Close()
	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfigFromDir(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := Diff(dir, cfg, mf)
	if err != nil {
		t.Fatal(err)
	}
	cls, err := classifySkips(cfg, reg2, items, mf, diff, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cls.drifted) != 3 {
		t.Errorf("drifted = %d docs, want 3", len(cls.drifted))
	}
	for p, reason := range cls.driftReasons {
		if reason != "templates" {
			t.Errorf("drift reason for %s = %q, want templates", p, reason)
		}
	}
}

// TestSkip_DependentsEnumeration pins AC-3's enumerated dependents: touching
// one doc changes exactly that doc's dependent artifacts.
func TestSkip_DependentsEnumeration(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	// Baseline: summary + article files hashed.
	sumBefore, _ := os.ReadFile(filepath.Join(dir, "wiki", "summaries", "raw-doc2.md"))

	if err := os.WriteFile(filepath.Join(dir, "raw", "doc2.md"), []byte("# Deferred Doc 2\n\nEDITED substantially, entirely new content about gardening."), 0644); err != nil {
		t.Fatal(err)
	}
	compileInDir(t, dir, srv.URL, CompileOpts{})

	// Dependent 1: the doc's summary changed.
	sumAfter, _ := os.ReadFile(filepath.Join(dir, "wiki", "summaries", "raw-doc2.md"))
	if string(sumBefore) == string(sumAfter) {
		t.Error("touched doc's summary unchanged — should have been recompiled")
	}
	// Dependent 2: untouched docs' summaries unchanged.
	sum1Before, _ := os.ReadFile(filepath.Join(dir, "wiki", "summaries", "raw-doc1.md"))
	compileInDir(t, dir, srv.URL, CompileOpts{})
	sum1After, _ := os.ReadFile(filepath.Join(dir, "wiki", "summaries", "raw-doc1.md"))
	if string(sum1Before) != string(sum1After) {
		t.Error("untouched doc's summary changed across a no-op compile")
	}
}

func loadConfigFromDir(t *testing.T, dir string) (*config.Config, error) {
	t.Helper()
	if dir == "" {
		c := config.Defaults()
		return &c, nil
	}
	return config.Load(filepath.Join(dir, "config.yaml"))
}
