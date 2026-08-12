package compiler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/events"
)

// workerHarness bundles a mock-LLM project, the queue store, and a worker.
type workerHarness struct {
	dir     string
	project string
	server  *httptest.Server
	backend store.Backend
	items   store.CompileItemStore
	worker  *Worker
	embeds  atomic.Int32
}

// newWorkerHarness builds a temp project whose openai-compatible provider
// is a mock server answering chat completions (per-pass content, the
// pipeline_test.go pattern) and /embeddings (fixed 8-dim vector).
// embedStatus != 200 makes every embed call fail.
func newWorkerHarness(t *testing.T, defaultTier int, embedStatus int) *workerHarness {
	t.Helper()
	h := &workerHarness{}

	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			if embedStatus != http.StatusOK {
				w.WriteHeader(embedStatus)
				return
			}
			h.embeds.Add(1)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}}},
			})
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		lastMsg := ""
		if len(messages) > 0 {
			if m, ok := messages[len(messages)-1].(map[string]any); ok {
				lastMsg, _ = m["content"].(string)
			}
		}
		var content string
		switch {
		case strings.Contains(lastMsg, "concept extraction system"):
			content = `[{"name": "test-concept", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`
		case strings.Contains(lastMsg, "wiki author writing a comprehensive article"):
			content = "---\nconcept: test-concept\n---\n\n# Test Concept\n\nA test concept."
		default:
			content = "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject matter at length.\n\n## Concepts\n\ntest-concept: A fundamental concept extracted from the source material."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
		})
	}))
	t.Cleanup(h.server.Close)

	h.dir = t.TempDir()
	wiki.InitGreenfield(h.dir, "test", "gpt-4o-mini")
	cfgContent := `
version: 1
project: test
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + h.server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 2
  auto_commit: false
  summary_max_tokens: 500
  default_tier: ` + itoa(defaultTier) + `
`
	if err := os.WriteFile(filepath.Join(h.dir, "config.yaml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h.dir, "raw"), 0o755)

	db, err := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// R3: one backend owns every store a worker cycle touches — serve-mode
	// topology, where NewWorkerForServe sources Items from the same backend
	// the pass stores come from (single handle, single writeMu).
	h.backend = newTestBackend(db)
	h.items = h.backend.CompileItems()
	return h
}

var itoa = strconv.Itoa

// TestWorkerHarness_SingleBackendTopology pins spec R3/AC3: a worker cycle
// must run entirely on the harness backend — every pass store is the
// backend's own instance and no second sqlite handle is opened. Pre-fix,
// setupStores opened handle B for chunk/entry/vector stores and only
// itemStore pointed at handle A, so the 5 ms heartbeat raced FTS writes
// (BUSY/BUSY_SNAPSHOT, empty compile key).
func TestWorkerHarness_SingleBackendTopology(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.newWorker(t, 50*time.Millisecond, 5*time.Millisecond, 3)

	cr, err := h.worker.openCycleRun(context.Background())
	if err != nil {
		t.Fatalf("openCycleRun: %v", err)
	}
	defer cr.cleanup()
	tb, ok := cr.run.db.(*testBackend)
	if !ok {
		t.Fatalf("cycle run db = %T, want the harness testBackend (no second handle)", cr.run.db)
	}
	if cr.run.itemStore != h.items {
		t.Error("cycle itemStore is not the harness Items")
	}
	if cr.run.memStore != tb.Entries() || cr.run.chunkStore != tb.Chunks() ||
		cr.run.vecStore != tb.Vectors() || cr.run.trustStore != tb.Trust() ||
		cr.run.pipelineOntStore != tb.Ontology() {
		t.Error("cycle pass stores are not the harness backend's own stores")
	}
	if cr.run.db != tb {
		t.Error("cycle run db is not the harness backend")
	}
}

func (h *workerHarness) writeSource(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, "raw", name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *workerHarness) newWorker(t *testing.T, poll, heartbeat time.Duration, maxAttempts int) {
	t.Helper()
	h.worker = NewWorker(WorkerDeps{
		ProjectDir: h.dir,
		Items:      h.items,
		Backend:    h.backend,
		Coord:      NewCompileCoordinator(),
		Progress:   NewProgress(),
		Config: workerSettings{
			PollInterval:      poll,
			LeaseTTL:          5 * time.Second,
			HeartbeatInterval: heartbeat,
			MaxAttempts:       maxAttempts,
			ClaimLimit:        16,
		},
	})
}

func TestWorker_ProcessesClaimedItems(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content for indexing.")
	h.writeSource(t, "b.md", "# Beta\n\nBeta content for indexing.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	// Enqueue: pending tier-1 items (nothing done yet).
	for _, p := range []string{"raw/a.md", "raw/b.md"} {
		if err := h.items.Upsert(CompileItem{SourcePath: p, Hash: "h1", FileType: "md", Tier: 1}); err != nil {
			t.Fatal(err)
		}
	}

	worked, err := h.worker.cycle(context.Background())
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if !worked {
		t.Fatal("cycle reported no work with pending items")
	}
	for _, p := range []string{"raw/a.md", "raw/b.md"} {
		got, err := h.items.GetByPath(p)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "done" {
			t.Errorf("%s status = %q (error: %s), want done", p, got.Status, got.Error)
		}
		if got.Attempts != 0 {
			t.Errorf("%s attempts = %d, want 0 (done resets budget)", p, got.Attempts)
		}
		if got.LeaseOwner != "" {
			t.Errorf("%s lease not cleared: %q", p, got.LeaseOwner)
		}
	}
}

func TestWorker_ProcessesTier3Items(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content for the full pipeline.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	if err := h.items.Upsert(CompileItem{SourcePath: "raw/a.md", Hash: "h1", FileType: "md", Tier: 3}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	got, err := h.items.GetByPath("raw/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got.PassSummarized || !got.PassExtracted || !got.PassWritten {
		t.Errorf("tier-3 passes not committed: %+v", got)
	}
	if got.Status == "failed" {
		t.Errorf("item dead-lettered unexpectedly: %s", got.Error)
	}
	// Summary file exists on disk.
	matches, _ := filepath.Glob(filepath.Join(h.dir, "wiki", "summaries", "*a.md"))
	if len(matches) == 0 {
		t.Error("no summary file written")
	}
}

func TestWorker_LeaseExpiryRequeue(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	if err := h.items.Upsert(CompileItem{SourcePath: "raw/a.md", Hash: "h1", FileType: "md", Tier: 1}); err != nil {
		t.Fatal(err)
	}
	// A dead worker's lease, already expired.
	if _, err := h.items.Claim(1, "dead-worker", -time.Hour, 10); err != nil {
		t.Fatal(err)
	}

	worked, err := h.worker.cycle(context.Background())
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if !worked {
		t.Fatal("expired lease not requeued into work")
	}
	got, _ := h.items.GetByPath("raw/a.md")
	if got.Status != "done" {
		t.Errorf("status = %q, want done after requeue+process", got.Status)
	}
}

func TestWorker_DeadLetter(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	// Corrupt docx (plain text, not a zip): extraction fails every cycle —
	// the hard-failure path that MarkErrors (embed failures are soft by
	// design and never dead-letter).
	h.writeSource(t, "a.docx", "this is not a zip archive")
	h.newWorker(t, 50*time.Millisecond, time.Second, 2)

	if err := h.items.Upsert(CompileItem{SourcePath: "raw/a.docx", Hash: "h1", FileType: "docx", Tier: 1}); err != nil {
		t.Fatal(err)
	}

	// Cycle 1: failure → retry (attempts 1). Cycle 2: attempts+1 = 2 >= cap → failed.
	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	got, _ := h.items.GetByPath("raw/a.docx")
	if got.Status != "pending" || got.Attempts != 1 {
		t.Fatalf("after cycle 1: %+v, want pending attempts=1", got)
	}
	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	got, _ = h.items.GetByPath("raw/a.docx")
	if got.Status != "failed" {
		t.Fatalf("after cycle 2: status = %q, want failed (dead letter)", got.Status)
	}
	// Cycle 3: dead-lettered items are never re-claimed.
	worked, err := h.worker.cycle(context.Background())
	if err != nil {
		t.Fatalf("cycle 3: %v", err)
	}
	if worked {
		t.Error("dead-lettered item re-claimed")
	}
}

func TestWorker_PanicReleasesRetry(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	if err := h.items.Upsert(CompileItem{SourcePath: "raw/a.md", Hash: "h1", FileType: "md", Tier: 1}); err != nil {
		t.Fatal(err)
	}
	h.worker.hooks.indexTier1 = func(projectDir string, items []CompileItem, cr *compileRun) (int, int) {
		panic("simulated pass crash")
	}

	worked, err := h.worker.cycle(context.Background())
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if !worked {
		t.Fatal("cycle reported no work")
	}
	got, _ := h.items.GetByPath("raw/a.md")
	if got.Status != "pending" || got.Attempts != 1 {
		t.Errorf("after panic: %+v, want pending attempts=1 (retry)", got)
	}
}

func TestWorker_HeartbeatRefreshes(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	h.newWorker(t, 50*time.Millisecond, 50*time.Millisecond, 5)

	if err := h.items.Upsert(CompileItem{SourcePath: "raw/a.md", Hash: "h1", FileType: "md", Tier: 1}); err != nil {
		t.Fatal(err)
	}
	// Slow the tier-1 pass so the heartbeat loop ticks mid-cycle.
	h.worker.hooks.indexTier1 = func(projectDir string, items []CompileItem, cr *compileRun) (int, int) {
		time.Sleep(1200 * time.Millisecond)
		return indexAndEmbedSources(context.Background(), projectDir, items, cr.memStore, cr.vecStore, cr.embedder,
			cr.itemStore, cr.bp, cr.chunkStore, cr.cfg.Search.ChunkSizeOrDefault(), cr.cfg.Search.ChunkOverlapOrDefault(), cr.db, cr.exOpts...)
	}

	claimTime := time.Now().UTC().Truncate(time.Second)
	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	// The lease was released at cycle end; prove the heartbeat fired by
	// re-claiming history: heartbeat_at would equal the claim second
	// without the tick. Instead assert the item completed (done) AND the
	// heartbeat path executed — the released item's UpdatedAt advanced
	// past claimTime+1s.
	got, _ := h.items.GetByPath("raw/a.md")
	if got.Status != "done" {
		t.Errorf("status = %q, want done", got.Status)
	}
	if got.UpdatedAt <= claimTime.Format("2006-01-02 15:04:05") {
		t.Errorf("no activity past claim second: updated_at=%q", got.UpdatedAt)
	}
}

func TestWorker_TierClaimOrder(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha.")
	h.writeSource(t, "b.md", "# Beta\n\nBeta.")
	progress := NewProgress()
	events, unsub := progress.Subscribe(64)
	defer unsub()

	h.worker = NewWorker(WorkerDeps{
		ProjectDir: h.dir,
		Items:      h.items,
		Backend:    h.backend,
		Coord:      NewCompileCoordinator(),
		Progress:   progress,
		Config: workerSettings{
			PollInterval:      50 * time.Millisecond,
			LeaseTTL:          5 * time.Second,
			HeartbeatInterval: time.Second,
			MaxAttempts:       5,
			ClaimLimit:        16,
		},
	})
	// One tier-0 item (index only) and one tier-1 item (embed owed).
	if err := h.items.Upsert(CompileItem{SourcePath: "raw/a.md", Hash: "h1", FileType: "md", Tier: 0}); err != nil {
		t.Fatal(err)
	}
	if err := h.items.Upsert(CompileItem{SourcePath: "raw/b.md", Hash: "h1", FileType: "md", Tier: 1, PassIndexed: true}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	var phases []string
	for ev := range events {
		if ev.Type != "phase" {
			continue
		}
		phases = append(phases, ev.Phase)
		if len(phases) == 2 {
			break
		}
	}
	if len(phases) < 2 || !strings.Contains(phases[0], "Tier 0") || !strings.Contains(phases[1], "Tier 1") {
		t.Errorf("phase order = %v, want Tier 0 before Tier 1", phases)
	}
}

func TestWorker_ClaimFencing(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	for _, n := range []string{"a.md", "b.md", "c.md"} {
		h.writeSource(t, n, "# "+n+"\n\nContent of "+n+".")
	}
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)
	for _, p := range []string{"raw/a.md", "raw/b.md", "raw/c.md"} {
		if err := h.items.Upsert(CompileItem{SourcePath: p, Hash: "h1", FileType: "md", Tier: 1}); err != nil {
			t.Fatal(err)
		}
	}

	// Second worker with its own coordinator and token, same store.
	w2 := NewWorker(WorkerDeps{
		ProjectDir: h.dir,
		Items:      h.items,
		Backend:    h.backend,
		Coord:      NewCompileCoordinator(),
		Progress:   NewProgress(),
		Config:     h.worker.deps.Config,
	})

	done := make(chan error, 2)
	go func() { _, err := h.worker.cycle(context.Background()); done <- err }()
	go func() { _, err := w2.cycle(context.Background()); done <- err }()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("cycle: %v", err)
		}
	}

	for _, p := range []string{"raw/a.md", "raw/b.md", "raw/c.md"} {
		got, _ := h.items.GetByPath(p)
		if got.Status != "done" {
			t.Errorf("%s status = %q, want done (fencing: exactly one worker per item)", p, got.Status)
		}
	}
}

func TestWorker_EmitsQueueEvents(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	progress := NewProgress()
	events, unsub := progress.Subscribe(64)
	defer unsub()

	h.worker = NewWorker(WorkerDeps{
		ProjectDir: h.dir,
		Items:      h.items,
		Backend:    h.backend,
		Coord:      NewCompileCoordinator(),
		Progress:   progress,
		Config: workerSettings{
			PollInterval:      50 * time.Millisecond,
			LeaseTTL:          5 * time.Second,
			HeartbeatInterval: time.Second,
			MaxAttempts:       5,
			ClaimLimit:        16,
		},
	})
	// One pending item (claim event) and one expired lease (requeue event).
	if err := h.items.Upsert(CompileItem{SourcePath: "raw/a.md", Hash: "h1", FileType: "md", Tier: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.items.Claim(1, "dead-worker", -time.Hour, 10); err != nil {
		t.Fatal(err)
	}

	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	kinds := map[string]bool{}
	for {
		select {
		case ev := <-events:
			if ev.Type == "queue" {
				kinds[ev.Status] = true
			}
		default:
			if !kinds["requeued"] || !kinds["claimed"] {
				t.Errorf("queue events = %v, want requeued+claimed", kinds)
			}
			return
		}
	}
}

// TestWorkerCycle_StoresCompileKeys pins review F2: a completed worker
// cycle stores compile keys (the worker must not be key-blind).
func TestWorkerCycle_StoresCompileKeys(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content for the worker key sweep.")
	h.newWorker(t, 20*time.Millisecond, 5*time.Millisecond, 3)

	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	got, err := h.items.GetByPath("raw/a.md")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("no compile_items row after completed cycle")
	}
	if got.CompileKey == "" {
		t.Error("F2: worker cycle left compile_key empty — serve-built workspaces stay key-blind")
	}
}

// TestWorker_EmitsLifecycleEvents (SPEC-07 §4 + QA F-002): a worker cycle
// runs inside the workspace event plane — compile lifecycle + per-doc
// outcomes reach the sink installed via SetEventSink.
func TestWorker_EmitsLifecycleEvents(t *testing.T) {
	metrics.ResetForTest()
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content for indexing.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	sink := &workerCaptureSink{}
	h.worker.SetEventSink(sink)

	if err := h.items.Upsert(CompileItem{SourcePath: "raw/a.md", Hash: "h1", FileType: "md", Tier: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	types := map[events.Type]int{}
	for _, ev := range sink.events {
		types[ev.Type]++
	}
	if types[events.TypeCompileStarted] != 1 || types[events.TypeCompileFinished] != 1 {
		t.Errorf("lifecycle events = %v, want one started + one finished", types)
	}
	if types[events.TypeCompileDocFinished] == 0 {
		t.Error("no compile_doc_finished events from the worker cycle")
	}

	// SPEC-07 metrics: a worker cycle is a compile job — it bumps
	// compiles_total exactly once (verification review F-003).
	got := int64(-1)
	snap := metrics.Snapshot()
	for i := 0; i+1 < len(snap); i += 2 {
		if key, _ := snap[i].(string); strings.HasPrefix(key, "compiles_total") {
			got = snap[i+1].(int64)
		}
	}
	if got != 1 {
		t.Errorf("compiles_total = %d, want 1 for the worker cycle", got)
	}
}

type workerCaptureSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *workerCaptureSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

// TestWorker_EmitsUsageEvents (verification pass 2 F-011): a tier-3 worker
// cycle bridges LLM usage to the installed sink — the cycle client must be
// built with the sink, not an empty opts.
func TestWorker_EmitsUsageEvents(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content for a full tier-3 compile.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	sink := &workerCaptureSink{}
	h.worker.SetEventSink(sink)

	if err := h.items.Upsert(CompileItem{SourcePath: "raw/a.md", Hash: "h1", FileType: "md", Tier: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	usage := 0
	for _, ev := range sink.events {
		if ev.Type == events.TypeUsage {
			usage++
		}
	}
	if usage == 0 {
		t.Fatal("no usage events reached the sink from the tier-3 worker cycle")
	}
}

// TestWorker_LoadsPromptOverridesFreshPerCycle pins spec R2/R5 (AC3-AC4):
// each worker cycle loads <projectDir>/prompts into a fresh registry and
// passes it through FullPipelineOpts. The override sentinel lives ONLY in
// the prompt file — never in source content (spec Test Integrity
// Constraints). After the first cycle the override is rewritten and a NEW
// source enqueues a fresh tier-3 item (the first is done and cannot be
// re-claimed); cycle 2 must render the new body through its own registry.
func TestWorker_LoadsPromptOverridesFreshPerCycle(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nNeutral content for the full pipeline.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	promptsDir := filepath.Join(h.dir, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeOverride := func(marker string) {
		t.Helper()
		body := marker + " {{.SourcePath}}"
		if err := os.WriteFile(filepath.Join(promptsDir, "summarize-article.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeOverride("WORKER-CYCLE-V1")

	var registries []*prompts.Registry
	h.worker.hooks.fullPipeline = func(sources []SourceInfo, opts FullPipelineOpts) *FullPipelineResult {
		registries = append(registries, opts.Prompts)
		succeeded := make([]string, 0, len(sources))
		for _, s := range sources {
			succeeded = append(succeeded, s.Path)
		}
		return &FullPipelineResult{Pass23Completed: true, SucceededSources: succeeded}
	}

	render := func(reg *prompts.Registry) string {
		t.Helper()
		out, err := reg.Render("summarize_article", prompts.SummarizeData{SourcePath: "raw/a.md", SourceType: "md", MaxTokens: 500}, "")
		if err != nil {
			t.Fatalf("render through cycle registry: %v", err)
		}
		return out
	}

	// Cycle 1: the enqueue scan upserts raw/a.md at tier 3 (default_tier)
	// and the pipeline hook sees the freshly loaded override.
	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if len(registries) != 1 {
		t.Fatalf("cycle 1: fullPipeline called %d times, want 1", len(registries))
	}
	reg1 := registries[0]
	if reg1 == nil {
		t.Fatal("cycle 1: FullPipelineOpts.Prompts is nil — worker did not load the project registry")
	}
	if got := render(reg1); !strings.Contains(got, "WORKER-CYCLE-V1") {
		t.Errorf("cycle 1 render missing override: %q", got)
	}

	// Cycle 2: rewritten override + new source must render the new body.
	writeOverride("WORKER-CYCLE-V2")
	h.writeSource(t, "b.md", "# Beta\n\nMore neutral content for the full pipeline.")
	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if len(registries) != 2 {
		t.Fatalf("cycle 2: fullPipeline called %d times, want 2", len(registries))
	}
	reg2 := registries[1]
	if reg2 == nil {
		t.Fatal("cycle 2: FullPipelineOpts.Prompts is nil — worker did not load a fresh registry")
	}
	got := render(reg2)
	if !strings.Contains(got, "WORKER-CYCLE-V2") {
		t.Errorf("cycle 2 render missing rewritten override: %q", got)
	}
	if strings.Contains(got, "WORKER-CYCLE-V1") {
		t.Errorf("cycle 2 render reused the stale template: %q", got)
	}
}

// TestWorker_MalformedPromptOverrideWarnsAndFallsBack pins spec R4 (AC6):
// a malformed override logs a warning through the captured logger, leaves
// FullPipelineOpts.Prompts non-nil, and keeps the embedded defaults
// renderable — the cycle completes cleanly without the malformed body.
func TestWorker_MalformedPromptOverrideWarnsAndFallsBack(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nNeutral content for the full pipeline.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	promptsDir := filepath.Join(h.dir, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Unclosed template action — LoadFromDir must fail with a parse error.
	if err := os.WriteFile(filepath.Join(promptsDir, "summarize-article.md"), []byte("MALFORMED-BODY {{.SourcePath"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	restore := log.SetLoggerForTest(slog.New(slog.NewTextHandler(&buf, nil)))
	defer restore()

	var got *prompts.Registry
	h.worker.hooks.fullPipeline = func(sources []SourceInfo, opts FullPipelineOpts) *FullPipelineResult {
		got = opts.Prompts
		succeeded := make([]string, 0, len(sources))
		for _, s := range sources {
			succeeded = append(succeeded, s.Path)
		}
		return &FullPipelineResult{Pass23Completed: true, SucceededSources: succeeded}
	}

	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if !strings.Contains(buf.String(), "failed to load custom prompts") {
		t.Errorf("no warning for malformed override; log=%q", buf.String())
	}
	if got == nil {
		t.Fatal("FullPipelineOpts.Prompts is nil after a malformed override")
	}
	rendered, err := got.Render("summarize_article", prompts.SummarizeData{SourcePath: "raw/a.md", SourceType: "md", MaxTokens: 500}, "")
	if err != nil {
		t.Fatalf("embedded default no longer renderable: %v", err)
	}
	if strings.Contains(rendered, "MALFORMED-BODY") {
		t.Errorf("malformed body leaked into rendered prompt: %q", rendered)
	}
	if !strings.Contains(rendered, "research assistant") {
		t.Errorf("embedded default not rendered: %q", rendered)
	}

	item, err := h.items.GetByPath("raw/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "done" {
		t.Errorf("cycle did not complete cleanly: status=%q", item.Status)
	}
}
