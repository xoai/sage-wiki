package compiler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
)

// P3-6 T6b: write-time supersession trigger and trust conflict emission.

func temporalTestStores(t *testing.T) (*ontology.Store, *trust.Store) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "temporal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ont := ontology.NewStore(db,
		ontology.ValidRelationNames(ontology.MergedRelations(
			[]config.RelationConfig{{Name: "works_at", Functional: true}})),
		ontology.ValidEntityTypeNames(ontology.MergedEntityTypes(nil)))
	return ont, trust.NewStore(db)
}

func temporalCfg() *config.Config {
	c := enabledCfg()
	c.Ontology.Relations = []config.RelationConfig{{Name: "works_at", Functional: true}}
	return c
}

func graphServer(t *testing.T, graph string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": graph}}},
			"model":   "m",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

const worksAtAcme = `{"entities":[
	{"name":"alice","type":"person","description":"A person."},
	{"name":"acme","type":"org","description":"A company."}
],"relations":[
	{"source":"alice","predicate":"works_at","target":"acme",
	 "evidence":"Alice works at Acme.","confidence":0.9}
]}`

const worksAtInitech = `{"entities":[
	{"name":"alice","type":"person","description":"A person."},
	{"name":"initech","type":"org","description":"Another company."}
],"relations":[
	{"source":"alice","predicate":"works_at","target":"initech",
	 "evidence":"Alice works at Initech.","confidence":0.9}
]}`

const worksAtLowConf = `{"entities":[
	{"name":"alice","type":"person","description":"A person."},
	{"name":"globex","type":"org","description":"Yet another."}
],"relations":[
	{"source":"alice","predicate":"works_at","target":"globex",
	 "evidence":"Alice might work at Globex.","confidence":0.5}
]}`

const contradictsGraph = `{"entities":[
	{"name":"acme","type":"org","description":"A company."},
	{"name":"initech","type":"org","description":"Another company."}
],"relations":[
	{"source":"acme","predicate":"contradicts","target":"initech",
	 "evidence":"These statements conflict.","confidence":0.95}
]}`

func runOneDoc(t *testing.T, ont *ontology.Store, ts store.TrustStore, cfg *config.Config, graph, doc string) []FunctionalSupersession {
	t.Helper()
	srv := graphServer(t, graph)
	_, sup := ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: doc, Summary: "text"}}, nil,
		cfg, triplesClient(t, srv.URL), false, t.TempDir(), nil, ts)
	return sup
}

func liveTargets(t *testing.T, ont *ontology.Store, entity string) []string {
	t.Helper()
	rels, err := ont.GetRelations(entity, ontology.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rels {
		out = append(out, r.TargetID)
	}
	return out
}

func conflicts(t *testing.T, ts *trust.Store) []*store.PendingOutput {
	t.Helper()
	rows, err := ts.ListByState(store.StateConflict)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestFunctionalSupersessionAutoApplies(t *testing.T) {
	ont, ts := temporalTestStores(t)
	cfg := temporalCfg()

	runOneDoc(t, ont, ts, cfg, worksAtAcme, "raw/a.md")
	sup := runOneDoc(t, ont, ts, cfg, worksAtInitech, "raw/b.md")

	targets := liveTargets(t, ont, "alice")
	if len(targets) != 1 || targets[0] != "initech" {
		t.Errorf("default read must return only the winner (initech), got %v", targets)
	}
	if len(sup) != 1 || sup[0].KeepTargetID != "initech" || sup[0].Predicate != "works_at" {
		t.Errorf("supersession not collected for sweep: %+v", sup)
	}
	// The loser is invalidated, not deleted: point-in-time read still sees it.
	hist, err := ont.GetRelations("alice", ontology.Outbound, "")
	_ = hist
	all, err := ont.AllRelations()
	if err != nil {
		t.Fatal(err)
	}
	var loser *store.Relation
	for i := range all {
		if all[i].TargetID == "acme" {
			loser = &all[i]
		}
	}
	if loser == nil || loser.ValidTo == "" || loser.InvalidatedBy == "" {
		t.Errorf("loser must be invalidated (not deleted): %+v", loser)
	}
	if n := len(conflicts(t, ts)); n != 0 {
		t.Errorf("auto-applied supersession must not raise a conflict, got %d", n)
	}
}

func TestFunctionalBelowThresholdRaisesConflict(t *testing.T) {
	ont, ts := temporalTestStores(t)
	cfg := temporalCfg()

	runOneDoc(t, ont, ts, cfg, worksAtAcme, "raw/a.md")
	runOneDoc(t, ont, ts, cfg, worksAtLowConf, "raw/b.md")

	targets := liveTargets(t, ont, "alice")
	if len(targets) != 2 {
		t.Errorf("below-threshold must leave both edges live, got %v", targets)
	}
	rows := conflicts(t, ts)
	if len(rows) != 1 {
		t.Fatalf("expected 1 trust conflict, got %d", len(rows))
	}
	if rows[0].CreatedAt.IsZero() {
		t.Error("conflict row must carry CreatedAt")
	}
	if rows[0].QuestionHash == "" || rows[0].AnswerHash == "" {
		t.Error("conflict row must carry hashes")
	}

	// Dedup: same conflict again → still one row.
	runOneDoc(t, ont, ts, cfg, worksAtLowConf, "raw/b.md")
	if n := len(conflicts(t, ts)); n != 1 {
		t.Errorf("re-run must dedup conflicts, got %d", n)
	}
}

func TestBareContradictsRaisesConflictOnly(t *testing.T) {
	ont, ts := temporalTestStores(t)
	cfg := temporalCfg()

	runOneDoc(t, ont, ts, cfg, contradictsGraph, "raw/a.md")

	if n := len(conflicts(t, ts)); n != 1 {
		t.Errorf("bare contradicts must raise exactly one conflict, got %d", n)
	}
	// And nothing is invalidated: the contradicts edge itself is live.
	if targets := liveTargets(t, ont, "acme"); len(targets) != 1 {
		t.Errorf("contradicts edge must stay live, got %v", targets)
	}
}

func TestSupersessionWithNilTrustStore(t *testing.T) {
	ont, _ := temporalTestStores(t)
	cfg := temporalCfg()

	runOneDoc(t, ont, nil, cfg, worksAtAcme, "raw/a.md")
	runOneDoc(t, ont, nil, cfg, worksAtInitech, "raw/b.md") // must not panic

	if targets := liveTargets(t, ont, "alice"); len(targets) != 1 || targets[0] != "initech" {
		t.Errorf("nil trust store: supersession must still auto-apply, got %v", targets)
	}
}

func TestTemporalDisabledNoTrigger(t *testing.T) {
	ont, ts := temporalTestStores(t)
	cfg := temporalCfg()
	off := false
	cfg.Ontology.Temporal.Enabled = &off

	runOneDoc(t, ont, ts, cfg, worksAtAcme, "raw/a.md")
	runOneDoc(t, ont, ts, cfg, worksAtInitech, "raw/b.md")

	if targets := liveTargets(t, ont, "alice"); len(targets) != 2 {
		t.Errorf("disabled: both edges must stay, got %v", targets)
	}
	if n := len(conflicts(t, ts)); n != 0 {
		t.Errorf("disabled: no conflicts, got %d", n)
	}
}
