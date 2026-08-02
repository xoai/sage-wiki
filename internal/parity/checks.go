package parity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/sqlitestore"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// ByteGolden is the byte-parity golden schema (SPEC-09 §2.4 §1).
type ByteGolden struct {
	GoldenFormatVersion int               `json:"golden_format_version"`
	ConfigSHA256        string            `json:"config_sha256"`
	Files               map[string]string `json:"files"`
	Manifest            string            `json:"manifest"`
	CompileItems        map[string]string `json:"compile_items"`
	Counts              map[string]int    `json:"counts"`
}

// CaptureByteSnapshot reads a built workspace into a ByteGolden.
func CaptureByteSnapshot(wsDir, goldenConfigPath string) (*ByteGolden, error) {
	files, err := treeHashes(filepath.Join(wsDir, "wiki"))
	if err != nil {
		return nil, err
	}
	mfJSON, err := NormalizeManifestJSON(filepath.Join(wsDir, ".manifest.json"))
	if err != nil {
		return nil, err
	}
	mh := sha256.Sum256(mfJSON)

	cfgRaw, err := os.ReadFile(goldenConfigPath)
	if err != nil {
		return nil, fmt.Errorf("golden config: %w", err)
	}
	ch := sha256.Sum256(cfgRaw)

	g := &ByteGolden{
		GoldenFormatVersion: 1,
		ConfigSHA256:        hex.EncodeToString(ch[:]),
		Files:               files,
		Manifest:            hex.EncodeToString(mh[:]),
		CompileItems:        map[string]string{},
		Counts:              map[string]int{},
	}

	backend, err := sqlitestore.OpenPath(filepath.Join(wsDir, ".sage", "wiki.db"), store.ModeReader, sqlitestore.Options{})
	if err != nil {
		return nil, fmt.Errorf("open workspace backend: %w", err)
	}
	defer backend.Close()

	// Read through the store interfaces (the escape-hatch guard's
	// sanctioned path — no raw handles).
	items := backend.CompileItems()
	for tier := 0; tier <= 3; tier++ {
		list, err := items.ListByTier(tier)
		if err != nil {
			return nil, fmt.Errorf("compile_items tier %d: %w", tier, err)
		}
		for _, it := range list {
			g.CompileItems[it.SourcePath] = fmt.Sprintf("t%d/%v%v%v%v%v",
				it.Tier, it.PassIndexed, it.PassEmbedded, it.PassSummarized, it.PassExtracted, it.PassWritten)
		}
	}

	if n, err := backend.Entries().Count(); err != nil {
		return nil, err
	} else {
		g.Counts["fts"] = n
	}
	if n, err := backend.Vectors().Count(); err != nil {
		return nil, err
	} else {
		g.Counts["vec"] = n
	}
	if n, err := backend.Chunks().Count(); err != nil {
		return nil, err
	} else {
		g.Counts["chunks"] = n
	}
	if n, err := backend.Ontology().EntityCount(""); err != nil {
		return nil, err
	} else {
		g.Counts["ontology"] = n
	}
	if n, err := backend.Ontology().RelationCount(); err != nil {
		return nil, err
	} else {
		g.Counts["relations"] = n
	}
	return g, nil
}

// CheckByteParity compares the workspace against the committed golden.
// It runs the capture TWICE (self-determinism guard) before comparing.
func CheckByteParity(wsDir, goldenConfigPath, goldenPath string) error {
	g1, err := CaptureByteSnapshot(wsDir, goldenConfigPath)
	if err != nil {
		return err
	}
	g2, err := CaptureByteSnapshot(wsDir, goldenConfigPath)
	if err != nil {
		return err
	}
	if j1, j2 := mustJSON(g1), mustJSON(g2); string(j1) != string(j2) {
		return fmt.Errorf("byte snapshot not self-deterministic")
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		return fmt.Errorf("read golden: %w", err)
	}
	var golden ByteGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		return fmt.Errorf("parse golden: %w", err)
	}
	if golden.GoldenFormatVersion != g1.GoldenFormatVersion {
		return fmt.Errorf("golden_format_version %d, suite expects %d", golden.GoldenFormatVersion, g1.GoldenFormatVersion)
	}
	return diffByteGolden(&golden, g1)
}

func diffByteGolden(want, got *ByteGolden) error {
	var problems []string
	if want.ConfigSHA256 != got.ConfigSHA256 {
		problems = append(problems, "config weights changed (config_sha256 differs) — a weights change is a golden change")
	}
	for _, path := range sortedKeys(want.Files) {
		gotHash, ok := got.Files[path]
		if !ok {
			problems = append(problems, fmt.Sprintf("missing file: %s", path))
		} else if gotHash != want.Files[path] {
			problems = append(problems, fmt.Sprintf("content differs: %s", path))
		}
	}
	for _, path := range sortedKeys(got.Files) {
		if _, ok := want.Files[path]; !ok {
			problems = append(problems, fmt.Sprintf("unexpected file: %s", path))
		}
	}
	if want.Manifest != got.Manifest {
		problems = append(problems, ".manifest.json (normalized) differs")
	}
	for _, path := range sortedKeys(want.CompileItems) {
		if got.CompileItems[path] != want.CompileItems[path] {
			problems = append(problems, fmt.Sprintf("compile_items[%s]: %q vs golden %q", path, got.CompileItems[path], want.CompileItems[path]))
		}
	}
	for _, name := range sortedKeys(want.Counts) {
		if got.Counts[name] != want.Counts[name] {
			problems = append(problems, fmt.Sprintf("count[%s]: %d vs golden %d", name, got.Counts[name], want.Counts[name]))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("byte parity failed:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// GraphEdge is one canonical edge row in graph.jsonl.
type GraphEdge struct {
	SourceID      string  `json:"source_id"`
	Relation      string  `json:"relation"`
	TargetID      string  `json:"target_id"`
	Evidence      string  `json:"evidence,omitempty"`
	Confidence    float64 `json:"confidence,omitempty"`
	SourceDoc     string  `json:"source_doc,omitempty"`
	ValidFrom     string  `json:"valid_from,omitempty"`
	ValidTo       string  `json:"valid_to,omitempty"`
	InvalidatedBy string  `json:"invalidated_by,omitempty"`
}

// CaptureGraphJSONL dumps the workspace's edges in canonical order.
func CaptureGraphJSONL(wsDir string) (string, error) {
	db, err := storage.Open(filepath.Join(wsDir, ".sage", "wiki.db"))
	if err != nil {
		return "", fmt.Errorf("open workspace db: %w", err)
	}
	defer db.Close()
	ont := ontology.NewStore(db, nil, nil)
	rels, err := ont.AllRelations()
	if err != nil {
		return "", err
	}
	edges := make([]GraphEdge, 0, len(rels))
	for _, r := range rels {
		edges = append(edges, GraphEdge{
			SourceID: r.SourceID, Relation: r.Relation, TargetID: r.TargetID,
			Evidence: r.Evidence, Confidence: r.Confidence, SourceDoc: r.SourceDoc,
			ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, InvalidatedBy: r.InvalidatedBy,
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].SourceID != edges[j].SourceID {
			return edges[i].SourceID < edges[j].SourceID
		}
		if edges[i].Relation != edges[j].Relation {
			return edges[i].Relation < edges[j].Relation
		}
		return edges[i].TargetID < edges[j].TargetID
	})
	var b strings.Builder
	for _, e := range edges {
		raw, err := json.Marshal(e)
		if err != nil {
			return "", err
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// CheckGraphParity compares the canonical edge dump against the golden.
func CheckGraphJSONL(wsDir, goldenPath string) error {
	got, err := CaptureGraphJSONL(wsDir)
	if err != nil {
		return err
	}
	got2, err := CaptureGraphJSONL(wsDir)
	if err != nil {
		return err
	}
	if got != got2 {
		return fmt.Errorf("graph dump not self-deterministic")
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		return fmt.Errorf("read graph golden: %w", err)
	}
	if got != string(raw) {
		return fmt.Errorf("graph parity failed:\n--- got ---\n%s\n--- golden ---\n%s", got, raw)
	}
	return nil
}

// RegenGoldens rewrites every golden from the built workspace. It REFUSES
// without SAGE_PARITY_FORCE=1 (a leaked env var can't silently rewrite).
func RegenGoldens(wsDir, goldenConfigPath, goldenDir string) error {
	if os.Getenv("SAGE_PARITY_FORCE") != "1" {
		return fmt.Errorf("regen-goldens refuses to overwrite without SAGE_PARITY_FORCE=1")
	}
	g, err := CaptureByteSnapshot(wsDir, goldenConfigPath)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(goldenDir, "byte-parity.json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	graph, err := CaptureGraphJSONL(wsDir)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(goldenDir, "graph.jsonl"), []byte(graph), 0o644); err != nil {
		return err
	}

	// graph-asof.json: the AsOf view at the golden epoch — entities and
	// the relations live at that instant.
	w, err := engine.Open(context.Background(), wsDir, engine.WithReadOnly())
	if err != nil {
		return err
	}
	defer w.Close()
	asof, err := CaptureAsOf(w, goldenEpoch)
	if err != nil {
		return err
	}
	raw, err = json.MarshalIndent(asof, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(goldenDir, "graph-asof.json"), append(raw, '\n'), 0o644)
}

// AsOfGolden is the graph-asof golden schema.
type AsOfGolden struct {
	GoldenFormatVersion int               `json:"golden_format_version"`
	AsOf                string            `json:"asof"`
	Entities            []engine.Entity   `json:"entities"`
	Relations           []engine.Relation `json:"relations"`
	// Post relations: the same query at a LATER instant (post-instant of
	// the corpus's contradiction), making the pinned temporal semantics
	// explicit rather than invisible-by-construction (review N3).
	PostAsOf      string            `json:"post_asof"`
	PostRelations []engine.Relation `json:"post_relations"`
}

// CaptureAsOf snapshots the graph AsOf view at t. Entities and relations
// are sorted (SPEC-04 D1, same key as CaptureGraphJSONL): the AsOf SQL view
// has no ORDER BY, so its list order is insertion order — an accident of
// scheduling, not semantic content. The semantic contract is the SET.
func CaptureAsOf(w *engine.Workspace, t time.Time) (*AsOfGolden, error) {
	ents, err := w.Graph().Entities(context.Background(), engine.GraphFilter{})
	if err != nil {
		return nil, err
	}
	rels, err := w.Graph().AsOf(t).Relations(context.Background(), engine.GraphFilter{})
	if err != nil {
		return nil, err
	}
	postT := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	postRels, err := w.Graph().AsOf(postT).Relations(context.Background(), engine.GraphFilter{})
	if err != nil {
		return nil, err
	}
	sortAsOfGolden(ents, rels, postRels)
	return &AsOfGolden{
		GoldenFormatVersion: 1,
		AsOf:                t.Format(time.RFC3339),
		Entities:            ents,
		Relations:           rels,
		PostAsOf:            postT.Format(time.RFC3339),
		PostRelations:       postRels,
	}, nil
}

// sortAsOfGolden sorts entities by ID and relations by
// (SourceID, Relation, TargetID) in place.
func sortAsOfGolden(ents []engine.Entity, relsLists ...[]engine.Relation) {
	sort.Slice(ents, func(i, j int) bool { return ents[i].ID < ents[j].ID })
	for _, rels := range relsLists {
		sort.Slice(rels, func(i, j int) bool {
			if rels[i].SourceID != rels[j].SourceID {
				return rels[i].SourceID < rels[j].SourceID
			}
			if rels[i].Relation != rels[j].Relation {
				return rels[i].Relation < rels[j].Relation
			}
			return rels[i].TargetID < rels[j].TargetID
		})
	}
}

// CheckAsOf compares the workspace's AsOf view against the golden.
func CheckAsOf(wsDir, goldenPath string) error {
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		return fmt.Errorf("read asof golden: %w", err)
	}
	var golden AsOfGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		return fmt.Errorf("parse asof golden: %w", err)
	}
	// Goldens recorded before the harness sorted the AsOf view compare by
	// SET: sort the recorded lists the same way CaptureAsOf does.
	sortAsOfGolden(golden.Entities, golden.Relations, golden.PostRelations)
	if golden.GoldenFormatVersion != 1 {
		return fmt.Errorf("asof golden_format_version %d, want 1", golden.GoldenFormatVersion)
	}
	asOfTime, err := time.Parse(time.RFC3339, golden.AsOf)
	if err != nil {
		return fmt.Errorf("parse golden asof: %w", err)
	}
	w, err := engine.Open(context.Background(), wsDir, engine.WithReadOnly())
	if err != nil {
		return err
	}
	defer w.Close()
	got, err := CaptureAsOf(w, asOfTime)
	if err != nil {
		return err
	}
	// Self-determinism guard, same as the other checks (review R-04).
	got2, err := CaptureAsOf(w, asOfTime)
	if err != nil {
		return err
	}
	if string(mustJSON(got)) != string(mustJSON(got2)) {
		return fmt.Errorf("asof view not self-deterministic")
	}
	if string(mustJSON(got.Entities)) != string(mustJSON(golden.Entities)) {
		return fmt.Errorf("asof entities differ:\n  want %s\n  got  %s", mustJSON(golden.Entities), mustJSON(got.Entities))
	}
	if string(mustJSON(got.Relations)) != string(mustJSON(golden.Relations)) {
		return fmt.Errorf("asof relations differ:\n  want %s\n  got  %s", mustJSON(golden.Relations), mustJSON(got.Relations))
	}
	if string(mustJSON(got.PostRelations)) != string(mustJSON(golden.PostRelations)) {
		return fmt.Errorf("post-asof relations differ:\n  want %s\n  got  %s", mustJSON(golden.PostRelations), mustJSON(got.PostRelations))
	}
	return nil
}

// mustJSON marshals v, panicking on error (test harness convenience).
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
