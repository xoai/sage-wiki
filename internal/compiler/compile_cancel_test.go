package compiler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// TestCompile_CancelledSourceResumes proves the P1-1 contract end-to-end: a
// compile cancelled mid-summarize returns promptly, does NOT count the cancelled
// source as an error or mark it compiled, and a subsequent compile reprocesses it
// to completion. Cancelled != failed.
func TestCompile_CancelledSourceResumes(t *testing.T) {
	var blocking atomic.Bool
	blocking.Store(true)
	started := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		// Scan ALL messages — the pass-identifying phrases live in the SYSTEM
		// message, not the trailing user prompt.
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

		isConcept := strings.Contains(msg, "concept extraction system")
		isArticle := strings.Contains(msg, "wiki author writing comprehensive")
		isSummarize := !isConcept && !isArticle

		// On the first compile, block the summarize call until the request's
		// context is cancelled — simulating a long in-flight LLM call the user
		// Ctrl-Cs. req.WithContext propagates the client cancel to r.Context().
		if isSummarize && blocking.Load() {
			once.Do(func() { close(started) })
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Second):
			}
		}

		var content string
		switch {
		case isConcept:
			content = `[{"name": "test-concept", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`
		case isArticle:
			content = "---\nconcept: test-concept\n---\n\n# Test Concept\n\nA sufficiently long test concept article body for validation."
		default:
			content = "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject at length.\n\n## Concepts\n\ntest-concept: A fundamental concept extracted from the source."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
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
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 1
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"),
		[]byte("# Attention\n\nSelf-attention computes contextual representations of tokens."), 0644)

	// First compile — cancel while blocked on the summarize call.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *CompileResult, 1)
	go func() {
		r, _ := Compile(dir, CompileOpts{Ctx: ctx})
		done <- r
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("summarize call never reached the server")
	}
	cancel()

	var r1 *CompileResult
	select {
	case r1 = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("compile did not return promptly after cancel")
	}
	if r1 != nil && r1.Summarized != 0 {
		t.Errorf("cancelled compile summarized %d, want 0", r1.Summarized)
	}
	if r1 != nil && r1.Errors != 0 {
		t.Errorf("cancelled source counted as %d errors; cancelled must not be failure", r1.Errors)
	}

	// Second compile — no cancel, server responds normally. The cancelled source
	// must be reprocessed (it was never marked compiled, never added to Failed).
	blocking.Store(false)
	r2, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	if r2.Summarized != 1 {
		t.Errorf("resume summarized %d, want 1 — cancelled source was not reprocessed", r2.Summarized)
	}
	// Resume runs to completion, not just Pass 1.
	if r2.ConceptsExtracted < 1 {
		t.Errorf("resume extracted %d concepts, want >= 1", r2.ConceptsExtracted)
	}
}

// TestCompile_CancelAfterSummarizeResumesRemainingPasses is the C1 regression:
// when a source finishes Pass 1 (summarize) but the compile is cancelled before
// Pass 2/3, that source must NOT be marked fully compiled — otherwise resume
// skips it and its concepts/articles are silently lost. Without the cancel guard
// on the extracted/written pass flags, the resume below produces 0 articles.
func TestCompile_CancelAfterSummarizeResumesRemainingPasses(t *testing.T) {
	var blockExtract atomic.Bool
	blockExtract.Store(true)
	extractStarted := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		isConcept := strings.Contains(msg, "concept extraction system")
		isArticle := strings.Contains(msg, "wiki author writing comprehensive")

		// All sources summarize (Pass 1 completes → every source is a summarize
		// success), then block the concept-extraction call (Pass 2) and cancel
		// there. This is the C1 window: sources are summarize-succeeded but Pass
		// 2/3 never complete, so they must NOT be marked fully compiled.
		if isConcept && blockExtract.Load() {
			once.Do(func() { close(extractStarted) })
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Second):
			}
		}

		var content string
		switch {
		case isConcept:
			// Distinct concept per source — the extraction prompt carries the
			// "### Source: <path>" header, so a source skipped on resume yields a
			// missing article. This is what makes the test detect C1.
			var cs []string
			if strings.Contains(msg, "raw/a.md") {
				cs = append(cs, `{"name":"alpha","aliases":[],"sources":["raw/a.md"],"type":"concept"}`)
			}
			if strings.Contains(msg, "raw/b.md") {
				cs = append(cs, `{"name":"beta","aliases":[],"sources":["raw/b.md"],"type":"concept"}`)
			}
			content = "[" + strings.Join(cs, ",") + "]"
		case isArticle:
			content = "---\nconcept: test-concept\n---\n\n# Test Concept\n\nA sufficiently long test concept article body for validation."
		default:
			content = "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject at length.\n\n## Concepts\n\ntest-concept: A fundamental concept extracted from the source."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
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
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 1
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"),
		[]byte("# Attention\n\nSelf-attention computes contextual representations of tokens."), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "b.md"),
		[]byte("# Flash Attention\n\nFlash attention optimizes memory access patterns."), 0644)

	// First compile: cancel once the second source's summarize is in flight (so the
	// first source is a summarize success heading into the cancelled Pass 2/3).
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 1)
	go func() {
		Compile(dir, CompileOpts{Ctx: ctx})
		done <- struct{}{}
	}()
	select {
	case <-extractStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("concept extraction never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("compile did not return after cancel")
	}

	// Resume: the source that summarized before the cancel must have its Pass 2/3
	// reprocessed — concepts extracted and an article written. Without the C1 fix
	// it was marked fully compiled and skipped here (0 articles).
	blockExtract.Store(false)
	r2, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	// Both sources must be fully compiled after resume. With the C1 bug the source
	// that summarized before the cancel is marked done and skipped, so its concept
	// is never extracted and its article file is missing.
	for _, name := range []string{"alpha", "beta"} {
		if _, err := os.Stat(filepath.Join(dir, "wiki", "concepts", name+".md")); err != nil {
			t.Errorf("resume missing article %s.md — a summarized-then-cancelled source was skipped (C1)", name)
		}
	}
	_ = r2
}

// TestCompile_ExtractionFailureResumesPasses covers the generalized invariant
// (not just cancellation): a NON-cancel total extraction failure (Pass 2 errors)
// must also leave the summarized sources resumable, not marked fully compiled.
func TestCompile_ExtractionFailureResumesPasses(t *testing.T) {
	var failExtract atomic.Bool
	failExtract.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		isConcept := strings.Contains(msg, "concept extraction system")
		isArticle := strings.Contains(msg, "wiki author writing comprehensive")

		// Fail Pass 2 (extraction) with a non-retryable 400 — no cancellation.
		if isConcept && failExtract.Load() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var content string
		switch {
		case isConcept:
			var cs []string
			if strings.Contains(msg, "raw/a.md") {
				cs = append(cs, `{"name":"alpha","aliases":[],"sources":["raw/a.md"],"type":"concept"}`)
			}
			if strings.Contains(msg, "raw/b.md") {
				cs = append(cs, `{"name":"beta","aliases":[],"sources":["raw/b.md"],"type":"concept"}`)
			}
			content = "[" + strings.Join(cs, ",") + "]"
		case isArticle:
			content = "---\nconcept: c\n---\n\n# C\n\nA sufficiently long test article body for validation."
		default:
			content = "## Key claims\n\nThis document discusses the main concepts and findings at length.\n\n## Concepts\n\nc: A fundamental concept."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
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
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 1
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"), []byte("# A\n\nAlpha content about attention."), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "b.md"), []byte("# B\n\nBeta content about memory."), 0644)

	// First compile: extraction fails (no cancel). Sources summarize but Pass 2/3
	// don't complete.
	r1, _ := Compile(dir, CompileOpts{})
	if r1.Errors == 0 {
		t.Errorf("expected a Pass-2 extraction error to be recorded, got Errors=0")
	}

	// Resume: extraction now succeeds; the sources must be reprocessed to articles.
	failExtract.Store(false)
	r2, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if _, err := os.Stat(filepath.Join(dir, "wiki", "concepts", name+".md")); err != nil {
			t.Errorf("resume missing article %s.md — an extraction-failed source was skipped (MAJOR 2)", name)
		}
	}
	_ = r2
}

// TestCompile_CancelDuringPass3NoOrphanState is the 5th-round CRITICAL regression:
// a cancel during Pass 3 (the concept is already extracted and added to the
// manifest, the article is not yet written) must NOT persist a concept that has no
// article on disk. The old surgical rollback RemoveSource'd the summarized source
// but left the concept in mf.Concepts (RemoveSource only deletes Sources), and
// mf.Save then wrote that orphan — a concept with no article that converges
// permanently article-less on resume. The redesign persists no new compile state on
// an incomplete run (skip mf.Save + MarkPass), so a cancelled Pass 3 leaves the
// manifest exactly as it was before the compile: no orphaned concept.
func TestCompile_CancelDuringPass3NoOrphanState(t *testing.T) {
	var blockWrite atomic.Bool
	blockWrite.Store(true)
	writeStarted := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		isConcept := strings.Contains(msg, "concept extraction system")
		isArticle := strings.Contains(msg, "wiki author writing comprehensive")

		// Pass 1 (summarize) and Pass 2 (extract) complete; block the Pass 3 article
		// write and cancel there. This is the cancel-during-Pass-3 window: the concept
		// has been added to the manifest, but no article exists yet.
		if isArticle && blockWrite.Load() {
			once.Do(func() { close(writeStarted) })
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Second):
			}
		}

		var content string
		switch {
		case isConcept:
			content = `[{"name":"alpha","aliases":[],"sources":["raw/a.md"],"type":"concept"}]`
		case isArticle:
			content = "---\nconcept: alpha\n---\n\n# Alpha\n\nA sufficiently long test concept article body for validation."
		default:
			content = "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject at length.\n\n## Concepts\n\nalpha: A fundamental concept extracted from the source."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
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
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 1
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"),
		[]byte("# Attention\n\nSelf-attention computes contextual representations of tokens."), 0644)

	// First compile: cancel once the Pass 3 article write is in flight.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 1)
	go func() {
		Compile(dir, CompileOpts{Ctx: ctx})
		done <- struct{}{}
	}()
	select {
	case <-writeStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("article write never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("compile did not return after cancel")
	}

	// Load-bearing invariant: no concept may be persisted in the manifest without its
	// article on disk. With the old surgical rollback the source was removed but the
	// concept "alpha" stayed in mf.Concepts and was saved — an orphan (FAIL-BEFORE).
	// The redesign skips mf.Save on the incomplete run, so the manifest carries no
	// concept at all (PASS-AFTER). A missing manifest file (nothing saved) is fine.
	if mf, err := manifest.Load(filepath.Join(dir, ".manifest.json")); err == nil {
		for name := range mf.Concepts {
			if _, statErr := os.Stat(filepath.Join(dir, "wiki", "concepts", name+".md")); statErr != nil {
				t.Errorf("orphaned concept %q persisted with no article after cancel-during-Pass-3", name)
			}
		}
	}

	// Resume to completion: the article must exist and the run must be clean. (This
	// end-to-end check passes both before and after the fix in the exact-name case —
	// CheckDuplicate skips the same-name cache entry so resume self-heals — but it
	// confirms the redesign still produces a complete wiki.)
	blockWrite.Store(false)
	r2, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "concepts", "alpha.md")); err != nil {
		t.Errorf("resume missing article alpha.md — cancelled Pass 3 was not reprocessed")
	}
	_ = r2
}

// TestCompile_ZeroNewConceptsConverges guards the completion-flag boundary: when
// extraction legitimately yields zero NEW concepts (e.g. all dedup-merge into
// existing ones), Pass 2/3 DID complete. The source must stay recorded in the
// manifest and converge — not get rolled back (RemoveSource) and re-summarized on
// every subsequent compile forever.
func TestCompile_ZeroNewConceptsConverges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		content := "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject at length.\n\n## Concepts\n\ntest-concept: A fundamental concept extracted from the source."
		if strings.Contains(msg, "concept extraction system") {
			content = `[]` // valid extraction, zero new concepts
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
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
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 1
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"), []byte("# A\n\nContent about attention."), 0644)

	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("first compile: %v", err)
	}
	// A zero-new-concept run is COMPLETED, so the source must remain in the
	// manifest. With the buggy flag it was RemoveSource'd (source vanishes and
	// re-summarizes on every compile — never converges).
	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if _, ok := mf.Sources["raw/a.md"]; !ok {
		t.Error("zero-concept run removed raw/a.md from the manifest — non-convergent rollback")
	}
}
