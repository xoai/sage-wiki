package query

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xoai/sage-wiki/internal/llm"
)

// TestQuery_RecordsUsageEvent is the SPEC-05 wiring test (F-008): a query
// against a fake provider lands usage events in the workspace ledger with
// pass "query", tier -1, and nil cost for an unpriced model.
func TestQuery_RecordsUsageEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "Attention is [[attention]]."}, "finish_reason": "stop"}},
			"model":   "test-model-unpriced",
			"usage":   map[string]int{"prompt_tokens": 42, "completion_tokens": 8, "total_tokens": 50},
		})
	}))
	t.Cleanup(srv.Close)

	dir, db, cleanup := guardProject(t, srv)
	defer cleanup()

	if _, err := Query(dir, "what is attention", "markdown", 5, QueryOpts{DB: db}); err != nil {
		t.Fatalf("Query: %v", err)
	}

	events, err := llm.ReadUsageLog(llm.NewFileRecorder(dir).Path())
	if err != nil {
		t.Fatalf("ReadUsageLog: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("query must record at least one usage event")
	}
	ev := events[len(events)-1]
	if ev.Pass != "query" {
		t.Errorf("Pass = %q, want query", ev.Pass)
	}
	if ev.Tier != llm.TierNotCompileScoped {
		t.Errorf("Tier = %d, want %d (not compile-scoped)", ev.Tier, llm.TierNotCompileScoped)
	}
	if ev.Provider != "openai" || ev.Model != "test-model-unpriced" {
		t.Errorf("identity = %s/%s, want openai/test-model-unpriced", ev.Provider, ev.Model)
	}
	if ev.Cost != nil {
		t.Errorf("unpriced model must record nil cost, got %v", ev.Cost)
	}
	if ev.InputTokens != 42 || ev.OutputTokens != 8 {
		t.Errorf("tokens = %d/%d, want 42/8", ev.InputTokens, ev.OutputTokens)
	}
	if ev.TS.IsZero() {
		t.Error("event must carry a timestamp")
	}
}
