package compiler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xoai/sage-wiki/internal/wiki"
)

// fakeBatchServer is an OpenAI-compatible fake covering the batch endpoints
// (POST /files, POST /batches, GET /batches/{id}, GET /files/{id}/content)
// plus /chat/completions for standard-path passes. Status is switchable per
// test; call counts assert no double-submit / no re-poll.
type fakeBatchServer struct {
	*httptest.Server
	status        atomic.Value // string: in_progress | completed | expired | failed
	pollCount     atomic.Int32
	submitCount   atomic.Int32
	resultsJSONL  atomic.Value // string: JSONL body for the output file
	failIDs       atomic.Value // map[string]bool: custom_ids that return an error result
	failPaths     atomic.Value // string: source path whose standard-path calls get a 500
	uploadedJSONL atomic.Value // string: last batch input JSONL uploaded to /files (multipart)
}

func newFakeBatchServer(t *testing.T) *fakeBatchServer {
	t.Helper()
	f := &fakeBatchServer{}
	f.status.Store("in_progress")
	f.resultsJSONL.Store("")
	f.failIDs.Store(map[string]bool{})
	f.failPaths.Store("")
	f.uploadedJSONL.Store("")

	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/files":
			// Capture the uploaded batch input JSONL (multipart form file) —
			// the batch prompt assertions (P1-6) need it; it's NOT on /batches.
			if file, _, err := r.FormFile("file"); err == nil {
				data, _ := io.ReadAll(file)
				file.Close()
				f.uploadedJSONL.Store(string(data))
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "file-input-1"})

		case r.Method == "POST" && r.URL.Path == "/batches":
			f.submitCount.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"id": "batch_test_1"})

		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/batches/"):
			f.pollCount.Add(1)
			status := f.status.Load().(string)
			resp := map[string]any{"id": "batch_test_1", "status": status}
			if status == "completed" {
				resp["output_file_id"] = "file-output-1"
			}
			json.NewEncoder(w).Encode(resp)

		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/files/") && strings.HasSuffix(r.URL.Path, "/content"):
			fmt.Fprint(w, f.resultsJSONL.Load().(string))

		case r.Method == "POST" && r.URL.Path == "/chat/completions":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			messages, _ := body["messages"].([]any)
			var all strings.Builder
			for _, mm := range messages {
				if m, ok := mm.(map[string]any); ok {
					if c, ok := m["content"].(string); ok {
						all.WriteString(c)
						all.WriteByte(' ')
					}
				}
			}
			msg := all.String()
			// Poisoned source path → 500 (standard-path failure injection).
			if fp := f.failPaths.Load().(string); fp != "" && strings.Contains(msg, fp) {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "poisoned: " + fp}})
				return
			}
			var content string
			switch {
			case strings.Contains(msg, "concept extraction system"):
				content = `[{"name": "test-concept", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`
			case strings.Contains(msg, "wiki author writing comprehensive"):
				content = "---\nconcept: test-concept\n---\n\n# Test Concept\n\nA sufficiently long test concept article body for validation."
			default:
				content = "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject at length.\n\n## Concepts\n\ntest-concept: A fundamental concept extracted from the source."
			}
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": content}}},
				"model":   "gpt-4o-mini",
				"usage":   map[string]int{"total_tokens": 100},
			})

		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// setResults programs the output-file JSONL: one line per custom_id, each a
// successful chat completion, except IDs present in failIDs which return an
// error result.
func (f *fakeBatchServer) setResults(customIDs []string) {
	fail := f.failIDs.Load().(map[string]bool)
	var b strings.Builder
	for _, id := range customIDs {
		if fail[id] {
			fmt.Fprintf(&b, `{"custom_id": %q, "response": {"status_code": 500, "body": {"error": {"message": "boom"}}}, "error": {"message": "boom"}}`, id)
			b.WriteByte('\n')
			continue
		}
		summary := "## Batch summary\n\nA sufficiently long batch-generated summary body discussing the source material at length for validation."
		fmt.Fprintf(&b, `{"custom_id": %q, "response": {"status_code": 200, "body": {"choices": [{"message": {"content": %q}}], "model": "gpt-4o-mini", "usage": {"total_tokens": 50}}}, "error": null}`, id, summary)
		b.WriteByte('\n')
	}
	f.resultsJSONL.Store(b.String())
}

// writeBatchProject creates a greenfield project with the fake server wired
// as the OpenAI base_url and the given sources on disk.
func writeBatchProject(t *testing.T, serverURL string, compilerExtra string, sources ...string) string {
	t.Helper()
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")
	cfg := `
version: 1
project: test
sources:
  - path: raw
    type: auto
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + serverURL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 1
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
` + compilerExtra
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	for _, s := range sources {
		content := "# " + s + "\n\nSelf-attention computes contextual representations of tokens across the sequence."
		if err := os.WriteFile(filepath.Join(dir, s), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSubmitBatch_WritesBatchStateOnly(t *testing.T) {
	fake := newFakeBatchServer(t)
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

	if _, err := Compile(dir, CompileOpts{Batch: true}); err != nil {
		t.Fatalf("compile: %v", err)
	}

	if fake.submitCount.Load() != 1 {
		t.Errorf("submitCount = %d, want 1", fake.submitCount.Load())
	}
	bcp, err := loadBatchCheckpoint(dir)
	if err != nil || bcp == nil || bcp.Batch == nil {
		t.Fatalf("batch-state.json missing or invalid: bcp=%+v err=%v", bcp, err)
	}
	if bcp.Batch.BatchID != "batch_test_1" {
		t.Errorf("BatchID = %q, want batch_test_1", bcp.Batch.BatchID)
	}
	if len(bcp.Pending) != 1 || bcp.Pending[0] != "raw/a.md" {
		t.Errorf("Pending = %v, want [raw/a.md]", bcp.Pending)
	}
	if _, err := os.Stat(legacyCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("compile-state.json must not be written (P1-3)")
	}
}

// TestResumeBatch_Ended_FreshFixture: batch-state.json from the CURRENT
// binary resumes to completion; the batch file is deleted after summaries
// are written; no legacy JSON appears; a follow-up compile does NOT re-enter
// the batch path (post-resume cleanliness, spec test 2d).
func TestResumeBatch_Ended_FreshFixture(t *testing.T) {
	fake := newFakeBatchServer(t)
	fake.status.Store("completed")
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md", "raw/b.md")

	idA, idB := batchIDForPath("raw/a.md"), batchIDForPath("raw/b.md")
	if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
		CompileID: "c1",
		Batch: &BatchState{
			BatchID:  "batch_test_1",
			Provider: "openai",
			Pass:     "summarize",
			PathByID: map[string]string{idA: "raw/a.md", idB: "raw/b.md"},
		},
		Pending: []string{"raw/a.md", "raw/b.md"},
	}); err != nil {
		t.Fatal(err)
	}
	fake.setResults([]string{idA, idB})

	r, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	if r.Summarized != 2 {
		t.Errorf("Summarized = %d, want 2", r.Summarized)
	}

	// Summaries on disk (default "full" naming flattens the source path).
	for _, name := range []string{"raw-a.md", "raw-b.md"} {
		if _, err := os.Stat(filepath.Join(dir, "wiki", "summaries", name)); err != nil {
			t.Errorf("summary %s not written: %v", name, err)
		}
	}
	// Batch file deleted; no legacy JSON in the fresh fixture.
	if _, err := os.Stat(batchCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("batch-state.json should be deleted after successful resume")
	}
	if _, err := os.Stat(legacyCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("no compile-state.json in the fresh fixture")
	}

	// Post-resume cleanliness: next compile must not re-enter the batch path.
	polls := fake.pollCount.Load()
	r2, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("follow-up compile: %v", err)
	}
	if fake.pollCount.Load() != polls {
		t.Error("follow-up compile polled a consumed batch — stale re-materialization")
	}
	if _, err := os.Stat(batchCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("batch-state.json re-materialized after clean resume")
	}
	_ = r2
}

// TestResumeBatch_Ended_LegacyFixture: a LEGACY compile-state.json with an
// in-flight batch is split and resumed on the FIRST compile of the new
// binary (no double-submit, no standard-compile of batch sources). The
// Batch-stripped legacy file is intentionally retained until a standard
// run's MigrateCheckpoint (spec D5) — assert it stays stripped.
func TestResumeBatch_Ended_LegacyFixture(t *testing.T) {
	fake := newFakeBatchServer(t)
	fake.status.Store("completed")
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

	idA := batchIDForPath("raw/a.md")
	writeLegacyState(t, dir, CompileState{
		CompileID: "legacy-1",
		Pass:      1,
		Pending:   []string{"raw/a.md"},
		Batch: &BatchState{
			BatchID:  "batch_test_1",
			Provider: "openai",
			Pass:     "summarize",
			PathByID: map[string]string{idA: "raw/a.md"},
		},
	})
	fake.setResults([]string{idA})

	r, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	if fake.submitCount.Load() != 0 {
		t.Error("legacy in-flight batch was double-submitted")
	}
	if fake.pollCount.Load() != 1 {
		t.Errorf("pollCount = %d, want 1 (immediate resume of the legacy batch)", fake.pollCount.Load())
	}
	if r.Summarized != 1 {
		t.Errorf("Summarized = %d, want 1", r.Summarized)
	}

	if _, err := os.Stat(batchCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("batch-state.json should be deleted after successful resume")
	}
	// Legacy retained, Batch-stripped (MigrateCheckpoint owns it on a later
	// standard run).
	legacy := readLegacyState(t, dir)
	if legacy.Batch != nil {
		t.Error("legacy JSON must remain Batch-stripped after resume")
	}
}

func TestResumeBatch_InProgress(t *testing.T) {
	fake := newFakeBatchServer(t) // status: in_progress
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

	if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
		Batch:   &BatchState{BatchID: "batch_test_1", Provider: "openai", Pass: "summarize"},
		Pending: []string{"raw/a.md"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := os.Stat(batchCheckpointPath(dir)); err != nil {
		t.Error("batch-state.json must survive an in-progress poll")
	}
}

func TestResumeBatch_ExpiredAndFailed(t *testing.T) {
	for _, status := range []string{"expired", "failed"} {
		t.Run(status, func(t *testing.T) {
			fake := newFakeBatchServer(t)
			fake.status.Store(status)
			dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

			if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
				Batch:   &BatchState{BatchID: "batch_test_1", Provider: "openai", Pass: "summarize"},
				Pending: []string{"raw/a.md"},
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := Compile(dir, CompileOpts{}); err != nil {
				t.Fatalf("compile: %v", err)
			}
			if _, err := os.Stat(batchCheckpointPath(dir)); !os.IsNotExist(err) {
				t.Errorf("batch-state.json should be deleted on %s", status)
			}

			// Recovery contract (spec test 6): a re-run resubmits cleanly.
			if _, err := Compile(dir, CompileOpts{Batch: true}); err != nil {
				t.Fatalf("resubmit compile: %v", err)
			}
			if fake.submitCount.Load() != 1 {
				t.Errorf("submitCount = %d after %s, want 1 (clean resubmit)", fake.submitCount.Load(), status)
			}
			bcp, err := loadBatchCheckpoint(dir)
			if err != nil || bcp == nil || bcp.Batch == nil {
				t.Errorf("new batch checkpoint missing after resubmit: %+v", bcp)
			}
		})
	}
}

// TestResumeBatch_FailedSourceRetriedViaDiff: a batch result with an error
// leaves the source OUT of the manifest, so the next compile's Diff
// re-includes it and the standard path processes it (spec test 10).
func TestResumeBatch_FailedSourceRetriedViaDiff(t *testing.T) {
	fake := newFakeBatchServer(t)
	fake.status.Store("completed")
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md", "raw/b.md")

	idA, idB := batchIDForPath("raw/a.md"), batchIDForPath("raw/b.md")
	if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
		Batch: &BatchState{
			BatchID:  "batch_test_1",
			Provider: "openai",
			Pass:     "summarize",
			PathByID: map[string]string{idA: "raw/a.md", idB: "raw/b.md"},
		},
		Pending: []string{"raw/a.md", "raw/b.md"},
	}); err != nil {
		t.Fatal(err)
	}
	fake.failIDs.Store(map[string]bool{idB: true})
	fake.setResults([]string{idA, idB})

	r, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	if r.Summarized != 1 || r.Errors != 1 {
		t.Errorf("Summarized=%d Errors=%d, want 1/1", r.Summarized, r.Errors)
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "summaries", "raw-b.md")); !os.IsNotExist(err) {
		t.Error("failed source b.md must not have a summary")
	}

	// Next compile: standard path picks b.md up via Diff (it was never
	// AddSource'd into the manifest). NOTE: a.md is also re-diffed —
	// resumeBatch's pre-existing AddSource(path, "", ...) records an empty
	// hash, so Diff sees a mismatch. That re-summarize predates P1-3 and is
	// out of scope here; what matters is b.md is NOT lost.
	r2, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("retry compile: %v", err)
	}
	if r2.Summarized < 1 {
		t.Errorf("retry Summarized = %d, want >= 1 (b.md retried via Diff)", r2.Summarized)
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "summaries", "raw-b.md")); err != nil {
		t.Error("raw-b.md summary missing after retry — failed batch source was lost")
	}
}

// TestCompile_LegacyBatchSplitWriteFails_Aborts: if the batch-state.json
// write fails during the split, Compile aborts with an error and the legacy
// JSON is untouched — never strand a batch ID (spec D2 abort-on-error).
func TestCompile_LegacyBatchSplitWriteFails_Aborts(t *testing.T) {
	fake := newFakeBatchServer(t)
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

	writeLegacyState(t, dir, CompileState{
		CompileID: "legacy-1",
		Pending:   []string{"raw/a.md"},
		Batch:     &BatchState{BatchID: "batch_x", Provider: "openai", Pass: "summarize"},
	})

	// Force the split write to fail: batch-state.json as a non-empty
	// DIRECTORY — rename(tmp -> path) cannot replace it.
	blocker := batchCheckpointPath(dir)
	if err := os.MkdirAll(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "keep"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Compile(dir, CompileOpts{})
	if err == nil {
		t.Fatal("expected Compile to abort on split-write failure")
	}

	// Legacy untouched (batch ID not stranded, no strip attempted).
	legacy := readLegacyState(t, dir)
	if legacy.Batch == nil || legacy.Batch.BatchID != "batch_x" {
		t.Error("legacy JSON must be untouched after split-write failure")
	}
	if fake.submitCount.Load() != 0 || fake.pollCount.Load() != 0 {
		t.Error("no provider calls should happen after a split-write abort")
	}
}

// TestCompile_DeadBatchCheckpointRemoved: a parseable batch-state.json with
// Batch == nil is dead state (no writer produces it) — Compile removes it
// and proceeds standard rather than reloading it forever (Gate-3 fix).
func TestCompile_DeadBatchCheckpointRemoved(t *testing.T) {
	fake := newFakeBatchServer(t)
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

	if err := saveBatchCheckpoint(dir, &BatchCheckpoint{CompileID: "dead", Pending: []string{"raw/a.md"}}); err != nil {
		t.Fatal(err)
	}

	r, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := os.Stat(batchCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("dead batch checkpoint should be removed")
	}
	if fake.pollCount.Load() != 0 {
		t.Error("dead checkpoint must not trigger a batch resume")
	}
	// The compile must proceed standard, not return early.
	if r.Summarized != 1 {
		t.Errorf("Summarized = %d, want 1 (standard compile after dead-checkpoint removal)", r.Summarized)
	}
}

// TestCompile_DeadBatchCheckpoint_DryRunRetains: under --dry-run the dead
// checkpoint is NOT removed (dry-run defers all checkpoint deletion) and the
// run falls through to the standard dry-run report.
func TestCompile_DeadBatchCheckpoint_DryRunRetains(t *testing.T) {
	fake := newFakeBatchServer(t)
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

	if err := saveBatchCheckpoint(dir, &BatchCheckpoint{CompileID: "dead", Pending: []string{"raw/a.md"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := Compile(dir, CompileOpts{DryRun: true}); err != nil {
		t.Fatalf("dry-run compile: %v", err)
	}
	if _, err := os.Stat(batchCheckpointPath(dir)); err != nil {
		t.Error("dry-run must not delete even a dead checkpoint")
	}
	if fake.pollCount.Load() != 0 {
		t.Error("dead checkpoint must not trigger a batch resume")
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "summaries", "raw-a.md")); !os.IsNotExist(err) {
		t.Error("dry-run wrote a summary")
	}
}

// #124: a silently incomplete result set (truncated tail) must hard-error
// and keep the checkpoint for re-poll — never consume it.
func TestResumeBatch_MissingResultsKeepsCheckpoint(t *testing.T) {
	fake := newFakeBatchServer(t)
	fake.status.Store("completed")
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md", "raw/b.md")

	idA, idB := batchIDForPath("raw/a.md"), batchIDForPath("raw/b.md")
	if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
		CompileID: "c1",
		Batch: &BatchState{
			BatchID:  "batch_test_1",
			Provider: "openai",
			Pass:     "summarize",
			PathByID: map[string]string{idA: "raw/a.md", idB: "raw/b.md"},
		},
		Pending: []string{"raw/a.md", "raw/b.md"},
	}); err != nil {
		t.Fatal(err)
	}
	// Only ONE of two results returned — the truncation case.
	fake.setResults([]string{idA})

	_, err := Compile(dir, CompileOpts{})
	if err == nil {
		t.Fatal("missing batch results must hard-error, not silently continue")
	}
	if !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "raw/b.md") {
		t.Errorf("error must name the missing source: %v", err)
	}
	// Checkpoint survives for re-poll — with content intact enough to retry.
	data, statErr := os.ReadFile(batchCheckpointPath(dir))
	if statErr != nil {
		t.Error("checkpoint must be kept for re-poll after a completeness failure")
	}
	if !strings.Contains(string(data), "batch_test_1") || !strings.Contains(string(data), "raw/b.md") {
		t.Errorf("checkpoint content incomplete for re-poll: %s", data)
	}
}
