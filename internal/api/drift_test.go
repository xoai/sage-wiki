package api

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	wikimcp "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/wiki"
	"gopkg.in/yaml.v3"
)

// The drift check (04 §Rules): the committed OpenAPI spec, the registered
// /v1 routes, and the MCP tool registry must provably agree. If any of the
// three moves without the others, this test fails CI.

// Tools deliberately NOT exposed as synchronous REST routes — they are
// accessed through the async job API (P4-2) or excluded for other reasons.
var driftExcludedTools = map[string]string{
	"wiki_compile":       "P4-2: async job API (/v1/jobs/compile)",
	"wiki_compile_topic": "P4-2: async job API (/v1/jobs/compile with topic)",
	"wiki_lint":          "P4-2: async job API (/v1/jobs/lint)",
	"wiki_query":         "issue #125: MCP-only for now — a /v1 route is a separate decision (would ripple to both published clients)",
}

// Routes whose REST-facing params are intentionally NOT a subset of the
// tool's argument names (04 rule 3 exception).
var driftParamAllowlist = map[string]string{
	"/v1/ontology/entities": "INT-05: REST presents {id,type,name} over wiki_add_ontology's entity_* arguments",
}

// Routes that do not map 1:1 to an MCP tool (async job endpoints, internal
// catch-all). Rule 3 (param ⊆ tool args) does not apply to these.
var driftNonToolRoutes = map[string]bool{
	"/v1/jobs/{kind}": true,
	"/v1/jobs/{id}":   true,
	"/v1/jobs":        true,
}

type openapiDoc struct {
	Paths map[string]map[string]any `yaml:"paths"`
}

func loadSpec(t *testing.T) openapiDoc {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	specPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "api", "openapi.yaml")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	var doc openapiDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse api/openapi.yaml: %v", err)
	}
	return doc
}

func toolArgsByName(t *testing.T) map[string][]string {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	srv, err := wikimcp.NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	out := map[string][]string{}
	for name, st := range srv.MCPServer().ListTools() {
		args := make([]string, 0, len(st.Tool.InputSchema.Properties))
		for arg := range st.Tool.InputSchema.Properties {
			args = append(args, arg)
		}
		sort.Strings(args)
		out[name] = args
	}
	return out
}

func testRouterRoutes(t *testing.T) []Route {
	t.Helper()
	return New(nil, &config.Config{}, t.TempDir(), nil).Routes()
}

// driftErrors computes every spec ⇄ route ⇄ tool mismatch (04 rules 1–4).
func driftErrors(routes []Route, spec openapiDoc, toolArgs map[string][]string) []string {
	var errs []string

	// Rule 1: every registry tool appears as a route target or is excluded.
	routed := map[string]bool{}
	for _, rt := range routes {
		routed[rt.Tool] = true
	}
	for name := range toolArgs {
		if !routed[name] {
			if _, excluded := driftExcludedTools[name]; !excluded {
				errs = append(errs, "rule 1: tool "+name+" has no /v1 route and is not on the exclusion list")
			}
		}
	}
	// Exclusions must name real tools — a renamed tool must rot the
	// exclusion loudly, not silently.
	for name := range driftExcludedTools {
		if _, ok := toolArgs[name]; !ok {
			errs = append(errs, "rule 1: exclusion list names unknown tool "+name+" (renamed? update the exclusion)")
		}
	}

	// Rule 2, routes → spec: every registered route exists in the spec.
	for _, rt := range routes {
		methods, ok := spec.Paths[rt.Path]
		if !ok {
			errs = append(errs, "rule 2: route "+rt.Method+" "+rt.Path+" missing from api/openapi.yaml")
			continue
		}
		if _, ok := methods[strings.ToLower(rt.Method)]; !ok {
			errs = append(errs, "rule 2: route "+rt.Method+" "+rt.Path+" has no "+strings.ToLower(rt.Method)+" entry in api/openapi.yaml")
		}
	}

	// Rule 2, spec → routes: every documented path+method is registered.
	registered := map[string]bool{}
	for _, rt := range routes {
		registered[strings.ToLower(rt.Method)+" "+rt.Path] = true
	}
	for path, methods := range spec.Paths {
		for method := range methods {
			if !registered[method+" "+path] {
				errs = append(errs, "rule 2: api/openapi.yaml documents "+method+" "+path+" but no such route is registered")
			}
		}
	}

	// Rule 3: route params ⊆ target tool's argument names (allowlist excepted;
	// non-tool routes like job endpoints are excluded — they don't map 1:1).
	for _, rt := range routes {
		if _, ok := driftParamAllowlist[rt.Path]; ok {
			continue
		}
		if driftNonToolRoutes[rt.Path] {
			continue
		}
		args, ok := toolArgs[rt.Tool]
		if !ok {
			errs = append(errs, "rule 3: route "+rt.Path+" targets unknown tool "+rt.Tool)
			continue
		}
		known := map[string]bool{}
		for _, a := range args {
			known[a] = true
		}
		for _, p := range rt.Params {
			if !known[p] {
				errs = append(errs, "rule 3: route "+rt.Path+" declares param "+p+" absent from "+rt.Tool+"'s arguments")
			}
		}
	}

	return errs
}

func TestDrift_SpecRoutesToolsAgree(t *testing.T) {
	spec := loadSpec(t)
	toolArgs := toolArgsByName(t)
	routes := testRouterRoutes(t)

	if errs := driftErrors(routes, spec, toolArgs); len(errs) > 0 {
		t.Fatalf("drift detected:\n%s", strings.Join(errs, "\n"))
	}
}

func TestDrift_Counts(t *testing.T) {
	toolArgs := toolArgsByName(t)
	if len(toolArgs) != 19 {
		t.Fatalf("tool count = %d, want 19", len(toolArgs))
	}
	routes := testRouterRoutes(t)
	if len(routes) != 20 {
		t.Fatalf("route count = %d, want 20", len(routes))
	}
	spec := loadSpec(t)
	if len(spec.Paths) != 19 {
		t.Fatalf("spec path count = %d, want 19", len(spec.Paths))
	}
}

// The check is worthless if it can't catch drift: registering a route
// without a spec entry must fail.
func TestDrift_CatchesUnspecedRoute(t *testing.T) {
	spec := loadSpec(t)
	toolArgs := toolArgsByName(t)
	routes := append(testRouterRoutes(t), Route{
		Method: "GET", Pattern: "/v1/bogus", Path: "/v1/bogus", Tool: ToolStatus,
	})
	errs := driftErrors(routes, spec, toolArgs)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "/v1/bogus") {
			found = true
		}
	}
	if !found {
		t.Fatalf("drift check did not catch an unspeced route (got %d errors: %v)", len(errs), errs)
	}
}
