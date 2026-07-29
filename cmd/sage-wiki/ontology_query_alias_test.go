package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// runOntologyQueryCmd invokes runOntologyQuery with the given flags,
// capturing stdout and stderr separately — the separation IS the contract
// under test: JSON on stdout, human notices on stderr.
func runOntologyQueryCmd(t *testing.T, flags map[string]string) (string, string, error) {
	t.Helper()
	cmd := *ontologyQueryCmd
	c := &cmd
	c.ResetFlags()
	c.Flags().String("entity", "", "")
	c.Flags().String("relation", "", "")
	c.Flags().String("direction", "outbound", "")
	c.Flags().Int("depth", 1, "")
	c.Flags().String("as-of", "", "")
	for k, v := range flags {
		if err := c.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s: %v", k, err)
		}
	}

	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wErr
	runErr := runOntologyQuery(c, nil)
	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = origOut, origErr
	outB, _ := io.ReadAll(rOut)
	rOut.Close()
	errB, _ := io.ReadAll(rErr)
	rErr.Close()
	return string(outB), string(errB), runErr
}

// TestOntologyCLITraverseResolvesAlias: `ontology query --entity <alias>` must
// traverse from the canonical entity, keep stdout parseable as one JSON
// document (the notice must not contaminate it), and say on stderr what it
// resolved — the CLI is the one consumer where silent resolution would
// gaslight a user who typed the alias deliberately.
func TestOntologyCLITraverseResolvesAlias(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"canon", "neighbor", "alias"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ont.AddRelation(store.Relation{
		ID: "r1", SourceID: "canon", TargetID: "neighbor", Relation: "extends",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ont.LinkAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "canon", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	stdout, stderr, runErr := runOntologyQueryCmd(t, map[string]string{"entity": "alias"})
	if runErr != nil {
		t.Fatalf("query --entity alias: %v", runErr)
	}

	var resp struct {
		OK   bool `json:"ok"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not one valid JSON document: %v\n%s", err, stdout)
	}
	found := false
	for _, e := range resp.Data {
		if e.ID == "neighbor" {
			found = true
		}
	}
	if !found {
		t.Errorf("canonical's rows missing — alias not resolved before traversal:\n%s", stdout)
	}
	if !strings.Contains(stderr, "alias") || !strings.Contains(stderr, "canon") {
		t.Errorf("stderr must name both the alias and the canonical, got %q", stderr)
	}
}

// P3-6: ontology query --as-of traverses only edges valid at the given time;
// an invalid value errors before traversal.
func TestOntologyCLIAsOf(t *testing.T) {
	dir := resolveVault(t, "config.yaml")
	withProject(t, dir, "config.yaml")

	b, ont, err := openResolveStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "old-b", "new-b"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: ontology.TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// old-b: valid 2020 → 2025 (dead now, alive in 2022). new-b: live.
	if err := ont.AddRelation(store.Relation{
		ID: "r-old", SourceID: "a", TargetID: "old-b", Relation: "extends",
		ValidFrom: "2020-01-01T00:00:00Z", ValidTo: "2025-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ont.AddRelation(store.Relation{
		ID: "r-new", SourceID: "a", TargetID: "new-b", Relation: "extends",
	}); err != nil {
		t.Fatal(err)
	}
	b.Close()

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	ids := func(stdout string) map[string]bool {
		t.Helper()
		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
		}
		out := map[string]bool{}
		for _, e := range resp.Data {
			out[e.ID] = true
		}
		return out
	}

	// Default: only the live edge.
	stdout, _, runErr := runOntologyQueryCmd(t, map[string]string{"entity": "a"})
	if runErr != nil {
		t.Fatal(runErr)
	}
	got := ids(stdout)
	if !got["new-b"] || got["old-b"] {
		t.Errorf("default traverse must return live edges only, got %v", got)
	}

	// as-of 2022: the then-valid edge appears instead.
	stdout, _, runErr = runOntologyQueryCmd(t, map[string]string{"entity": "a", "as-of": "2022-06-01T00:00:00Z"})
	if runErr != nil {
		t.Fatal(runErr)
	}
	got = ids(stdout)
	if !got["old-b"] || !got["new-b"] {
		t.Errorf("as-of 2022 must return both (new-b is empty-valid-from), got %v", got)
	}

	// Invalid as-of produces a structured error (CLIError prints ok:false
	// JSON and returns nil under --format json — the machine contract).
	stdout, _, runErr = runOntologyQueryCmd(t, map[string]string{"entity": "a", "as-of": "january"})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	var errResp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &errResp); err != nil {
		t.Fatalf("error output not JSON: %v\n%s", err, stdout)
	}
	if errResp.OK || !strings.Contains(errResp.Error, "RFC3339") {
		t.Errorf("invalid --as-of must produce ok:false naming RFC3339, got %s", stdout)
	}
}
