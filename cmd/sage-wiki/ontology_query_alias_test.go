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
