package parity

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/serve"
)

// TestServeSearchParity is AC-S2: REST /search returns identical ranked
// doc lists and scores to the SPEC-09 golden contract (the shared
// CLI/REST plug-in SPEC-09 reserved).
func TestServeSearchParity(t *testing.T) {
	if suiteWS == "" {
		t.Skip("shared workspace not built (SAGE_PARITY_FORCE=1 mode)")
	}
	// Pin the recency clock at the golden epoch for BOTH the golden and
	// the serve path (SOURCE_DATE_EPOCH, honored by mcp search).
	t.Setenv("SOURCE_DATE_EPOCH", "32503680000") // year 3000

	deps, err := serve.AssembleDeps(suiteWS)
	if err != nil {
		t.Fatal(err)
	}
	defer deps.Close()
	mcpSrv, err := mcppkg.NewServer(suiteWS, deps.Coordinator())
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSrv.Close()

	srv, err := serve.New(deps, mcpSrv, serve.Config{
		Workspace: suiteWS,
		ReadyFn:   func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	raw, err := os.ReadFile(goldenPath("search.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden SearchGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}

	for _, q := range golden.Queries {
		body, _ := json.Marshal(map[string]any{
			"query":    q.Q,
			"limit":    10,
			"channels": q.Channels,
		})
		resp, err := httpSrv.Client().Post(httpSrv.URL+"/search", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /search %q: %v", q.Q, err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("POST /search %q: status %d", q.Q, resp.StatusCode)
		}
		var got struct {
			Results []struct {
				ID          string  `json:"ID"`
				ArticlePath string  `json:"ArticlePath"`
				FinalScore  float64 `json:"FinalScore"`
			} `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode %q: %v", q.Q, err)
		}
		resp.Body.Close()

		if len(got.Results) != len(q.Expect) {
			t.Errorf("query %q: %d results vs golden %d", q.Q, len(got.Results), len(q.Expect))
			continue
		}
		for i, e := range q.Expect {
			g := got.Results[i]
			doc := g.ArticlePath
			if doc == "" {
				doc = g.ID
			}
			// The MCP wire carries no rank number — rank is the list order.
			if doc != e.Doc || i+1 != e.Rank || g.FinalScore != e.Score {
				t.Errorf("query %q row %d: got {%s %d %.17g} want {%s %d %.17g}",
					q.Q, i, doc, i+1, g.FinalScore, e.Doc, e.Rank, e.Score)
			}
		}
	}
}
