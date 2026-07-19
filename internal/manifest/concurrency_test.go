package manifest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// TestMutateCrossProcessHelper is not a real test — it is the subprocess body
// re-executed by TestMutateConcurrencyNoLostUpdate to produce genuine
// cross-process lock contention (in-process goroutines share the process and so
// cannot prove the file lock closes the cross-process window). It adds
// MANIFEST_TEST_COUNT sources under MANIFEST_TEST_PREFIX to the manifest at
// MANIFEST_TEST_PATH, each through Mutate, then exits. In a normal run the env
// vars are unset and it skips.
func TestMutateCrossProcessHelper(t *testing.T) {
	path := os.Getenv("MANIFEST_TEST_PATH")
	if path == "" {
		t.Skip("helper invoked outside its subprocess harness")
	}
	prefix := os.Getenv("MANIFEST_TEST_PREFIX")
	n, _ := strconv.Atoi(os.Getenv("MANIFEST_TEST_COUNT"))
	opts := fastLockOpts()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("%s-%d", prefix, i)
		if err := mutateWithOpts(context.Background(), path, opts, func(m *Manifest) error {
			m.AddSource(key, "sha256:sub", "article", 1)
			return nil
		}); err != nil {
			t.Fatalf("subprocess mutate %q: %v", key, err)
		}
	}
}

// TestMutateInterleaveDisjointKeysBothSurvive is the A2 disjoint case at the
// Mutate level — the contract every routed writer now relies on. A compile-style
// mutation (mark a source compiled + add its concept) and an MCP-style mutation
// (add an unrelated source) run concurrently through Mutate; whatever the
// interleave, neither clobbers the other because each reloads fresh under the
// lock.
func TestMutateInterleaveDisjointKeysBothSurvive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")

	seed := New()
	seed.AddSource("raw/paper.md", "sha256:seed", "paper", 100)
	if err := seed.Save(path); err != nil {
		t.Fatalf("seed: %v", err)
	}

	opts := fastLockOpts()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := mutateWithOpts(context.Background(), path, opts, func(m *Manifest) error {
			m.MarkCompiled("raw/paper.md", "wiki/summaries/paper.md", []string{"attention"})
			m.AddConcept("attention", "wiki/concepts/attention.md", []string{"raw/paper.md"})
			return nil
		}); err != nil {
			t.Errorf("compile-style mutate: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := mutateWithOpts(context.Background(), path, opts, func(m *Manifest) error {
			m.AddSource("raw/notes.md", "sha256:notes", "article", 50)
			return nil
		}); err != nil {
			t.Errorf("mcp-style mutate: %v", err)
		}
	}()
	wg.Wait()

	m, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s := m.Sources["raw/paper.md"]; s.Status != "compiled" {
		t.Errorf("compile mutation lost: status=%q", s.Status)
	}
	if _, ok := m.Concepts["attention"]; !ok {
		t.Error("compile concept lost")
	}
	if _, ok := m.Sources["raw/notes.md"]; !ok {
		t.Error("MCP source lost")
	}
}

// TestMutateConcurrencyNoLostUpdate is A1: N in-process goroutines plus a
// cross-process subprocess all mutate the same manifest concurrently, each
// adding a distinct source. Every mutation must survive (no lost update) and the
// final manifest must be parseable. Run under -race -count=10.
func TestMutateConcurrencyNoLostUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")

	const inProcN = 15
	const subProcN = 15
	opts := fastLockOpts()

	// Launch the cross-process contender first so it overlaps the goroutines.
	cmd := exec.Command(os.Args[0], "-test.run=^TestMutateCrossProcessHelper$")
	cmd.Env = append(os.Environ(),
		"MANIFEST_TEST_PATH="+path,
		"MANIFEST_TEST_PREFIX=subproc/src",
		"MANIFEST_TEST_COUNT="+strconv.Itoa(subProcN),
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < inProcN; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("inproc/src-%d", i)
			if err := mutateWithOpts(context.Background(), path, opts, func(m *Manifest) error {
				m.AddSource(key, "sha256:in", "article", 1)
				return nil
			}); err != nil {
				t.Errorf("in-process mutate %q: %v", key, err)
			}
		}(i)
	}
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("subprocess failed: %v", err)
	}

	// No lost update: every writer's source is present, and the file parses.
	m, err := Load(path)
	if err != nil {
		t.Fatalf("load final manifest: %v", err)
	}
	for i := 0; i < inProcN; i++ {
		if _, ok := m.Sources[fmt.Sprintf("inproc/src-%d", i)]; !ok {
			t.Errorf("lost in-process source %d", i)
		}
	}
	for i := 0; i < subProcN; i++ {
		if _, ok := m.Sources[fmt.Sprintf("subproc/src-%d", i)]; !ok {
			t.Errorf("lost subprocess source %d", i)
		}
	}
	if got := m.SourceCount(); got != inProcN+subProcN {
		t.Errorf("expected %d sources, got %d", inProcN+subProcN, got)
	}
}
