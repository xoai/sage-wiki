package events

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// TestEnvelopeJSON: the wire shape is {"id","time","workspace","type","data"}
// with time as RFC3339Nano UTC and data carrying the typed payload.
func TestEnvelopeJSON(t *testing.T) {
	ev := NewEvent("ws", TypeDocCaptured, DocCaptured{DocID: "raw/a.md", Bytes: 12, ContentHash: "sha256:ab"})
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "time", "workspace", "type", "data"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing top-level key %q in %s", key, raw)
		}
	}
	if len(got) != 5 {
		t.Errorf("top-level keys = %d, want exactly 5: %s", len(got), raw)
	}
	// Documented ID shape: <12 hex> "-" <16 hex>.
	if len(ev.ID) != 12+1+16 || ev.ID[12] != '-' {
		t.Errorf("ID %q does not match the documented <12hex>-<16hex> shape", ev.ID)
	}
	var ts time.Time
	if err := json.Unmarshal(got["time"], &ts); err != nil {
		t.Fatalf("time not RFC3339: %v", err)
	}
	if ts.Location() != time.UTC && !strings.HasSuffix(string(got["time"]), `Z"`) {
		t.Errorf("time must be UTC: %s", got["time"])
	}
	if string(got["type"]) != `"doc_captured"` {
		t.Errorf("type = %s, want \"doc_captured\"", got["type"])
	}
	if !strings.Contains(string(got["data"]), `"doc_id":"raw/a.md"`) {
		t.Errorf("data missing snake_case payload fields: %s", got["data"])
	}
}

// TestPayloadTypeMapping: every Type maps to exactly one payload type and
// vice versa — the union is closed.
func TestPayloadTypeMapping(t *testing.T) {
	types := []Type{
		TypeDocCaptured, TypeCompileStarted, TypeCompileDocFinished, TypeCompileFinished,
		TypeEdgeAdded, TypeEdgeInvalidated, TypeEntityResolved, TypePromotionTriggered,
		TypeSearchPerformed, TypeMirrorShipped, TypeMirrorSnapshot,
		TypeUsage, TypeCompileSkip, TypeEventsDropped,
		TypeLimitExceeded, TypeEdgeRejected,
	}
	seen := map[Type]bool{}
	payloadNames := map[string]Type{}
	for _, ty := range types {
		if seen[ty] {
			t.Errorf("duplicate Type in enumeration: %s", ty)
		}
		seen[ty] = true
		pt := PayloadType(ty)
		if pt == nil {
			t.Errorf("PayloadType(%s) = nil", ty)
			continue
		}
		if prev, dup := payloadNames[pt.Name()]; dup {
			t.Errorf("payload %s shared by %s and %s", pt.Name(), prev, ty)
		}
		payloadNames[pt.Name()] = ty
	}
	if len(types) != 16 {
		t.Errorf("union size = %d, want 16", len(types))
	}
	if PayloadType(Type("bogus")) != nil {
		t.Error("unknown Type must map to nil")
	}
}

// TestNewEventRejectsMismatchedPayload: the constructor is the boundary guard
// — a Type/payload mismatch is a programming error and fails loudly.
func TestNewEventRejectsMismatchedPayload(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewEvent with mismatched payload must panic")
		}
	}()
	NewEvent("ws", TypeDocCaptured, Usage{})
}

// TestNewEventRejectsPathWorkspace: Workspace is a name, never a path —
// the privacy rule is mechanical, not disciplinary.
func TestNewEventRejectsPathWorkspace(t *testing.T) {
	for _, bad := range []string{"/abs/ws", "some/dir", `c:\ws`} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewEvent with workspace %q must panic", bad)
				}
			}()
			NewEvent(bad, TypeEventsDropped, EventsDropped{Dropped: 1})
		}()
	}
}

// TestIDUniqueAndSortable: IDs are unique and their time prefix preserves
// generation order at millisecond resolution (same-ms ties are unordered by
// design — the suffix is random).
func TestIDUniqueAndSortable(t *testing.T) {
	seen := map[string]bool{}
	var prevPrefix string
	for i := 0; i < 1000; i++ {
		id := newID(time.Now().UTC())
		if seen[id] {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = true
		prefix := strings.Split(id, "-")[0]
		if prevPrefix != "" && prefix < prevPrefix {
			t.Fatalf("ID time prefixes not sortable: %q then %q", prevPrefix, prefix)
		}
		prevPrefix = prefix
	}
	// The prefix decodes back to the generation instant (millisecond resolution).
	now := time.Now().UTC()
	id := newID(now)
	ms, err := parseHexInt64(strings.Split(id, "-")[0])
	if err != nil {
		t.Fatalf("ID prefix not hex: %v", err)
	}
	if got, want := time.UnixMilli(ms).UTC(), now.Truncate(time.Millisecond); got != want {
		t.Errorf("ID prefix = %d (%s), want ms of %s", ms, got, want)
	}
}

// TestUsageCostNumber: cost serializes as a JSON number or null — never a
// quoted string, never a fabricated zero (SPEC-05 wire rule, carried over).
func TestUsageCostNumber(t *testing.T) {
	c := decimal.NewFromFloat(0.123)
	ev := NewEvent("ws", TypeUsage, Usage{Provider: "openai", Model: "gpt-4o-mini", Cost: &c})
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cost":0.123`) {
		t.Errorf("cost must be a JSON number: %s", raw)
	}

	evNil := NewEvent("ws", TypeUsage, Usage{Provider: "openai", Model: "unknown-model"})
	rawNil, err := json.Marshal(evNil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawNil), `"cost":null`) {
		t.Errorf("unknown cost must be null: %s", rawNil)
	}
}

// TestCompileFinishedCostNumber: the Totals cost follows the same rule.
func TestCompileFinishedCostNumber(t *testing.T) {
	c := decimal.NewFromFloat(1.5)
	ev := NewEvent("ws", TypeCompileFinished, CompileFinished{
		JobID: "j1", Outcome: "completed",
		Totals: CompileTotals{Docs: 2, Compiled: 2, Cost: &c},
	})
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cost":1.5`) {
		t.Errorf("totals cost must be a JSON number: %s", raw)
	}
}

// parseHexInt64 decodes the ID's time prefix (test helper).
func parseHexInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 16, 64)
}

// SPEC-08 D2: the two hardening event types join the closed union.

func TestLimitExceededJSONShape(t *testing.T) {
	ev := NewEvent("ws", TypeLimitExceeded, LimitExceeded{
		Which:  "doc_bytes",
		Limit:  10485760,
		Got:    20000000,
		Detail: "capture:raw/note.md",
	})
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round struct {
		Type string `json:"type"`
		Data struct {
			Which  string `json:"which"`
			Limit  int64  `json:"limit"`
			Got    int64  `json:"got"`
			Detail string `json:"detail"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.Type != "limit_exceeded" {
		t.Errorf("type = %q, want limit_exceeded", round.Type)
	}
	if round.Data.Which != "doc_bytes" || round.Data.Limit != 10485760 ||
		round.Data.Got != 20000000 || round.Data.Detail != "capture:raw/note.md" {
		t.Errorf("data = %+v, fields not pinned snake_case", round.Data)
	}
}

func TestEdgeRejectedJSONShape(t *testing.T) {
	ev := NewEvent("ws", TypeEdgeRejected, EdgeRejected{
		Source:    "alpha",
		Predicate: "uses",
		Target:    "beta",
		Reason:    "span_missing",
	})
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round struct {
		Type string `json:"type"`
		Data struct {
			Source    string `json:"source"`
			Predicate string `json:"predicate"`
			Target    string `json:"target"`
			Reason    string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.Type != "edge_rejected" {
		t.Errorf("type = %q, want edge_rejected", round.Type)
	}
	if round.Data.Source != "alpha" || round.Data.Predicate != "uses" ||
		round.Data.Target != "beta" || round.Data.Reason != "span_missing" {
		t.Errorf("data = %+v, fields not pinned snake_case", round.Data)
	}
}

func TestNewEventRejectsMismatchedSpec08Payloads(t *testing.T) {
	mustPanic := func(f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Error("NewEvent with mismatched payload must panic")
			}
		}()
		f()
	}
	mustPanic(func() { NewEvent("ws", TypeLimitExceeded, EdgeRejected{}) })
	mustPanic(func() { NewEvent("ws", TypeEdgeRejected, LimitExceeded{}) })
}
