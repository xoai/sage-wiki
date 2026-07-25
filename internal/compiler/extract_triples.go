package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/store"
)

// ExtractedEntity is one node the LLM found in a document.
type ExtractedEntity struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// ExtractedRelation is one evidenced edge between two extracted entities.
//
// Evidence is a span quoted from the document's COMPILED SUMMARY, not its raw
// source: Pass 2 operates on summaries. See store.Relation's field comment.
type ExtractedRelation struct {
	Source     string  `json:"source"`
	Predicate  string  `json:"predicate"`
	Target     string  `json:"target"`
	Evidence   string  `json:"evidence"`
	Confidence float64 `json:"confidence"`
}

// ExtractedGraph is one document's extracted subgraph.
type ExtractedGraph struct {
	Entities  []ExtractedEntity   `json:"entities"`
	Relations []ExtractedRelation `json:"relations"`
}

// TriplesSchema constrains the structured-output response (P3-2).
//
// Deliberately carries NO enum, on either `type` or `predicate`, even though
// both are drawn from a configured vocabulary. StructuredCompletion's fallback
// path validates the schema just as strictly as the native one
// (structured.go -> ValidateJSON), and an enum violation fails the WHOLE call —
// so one hallucinated predicate would cost the document's entire graph instead
// of one edge. The vocabulary is carried by the prompt and enforced in Go,
// where a bad value costs exactly one node or edge. ConceptsSchema makes the
// same choice for its `type` field.
//
// `required` must be a Go []string: schema.go type-asserts it, and a []any
// silently skips ALL required-field validation.
var TriplesSchema = llm.JSONSchema{
	Name:        "graph",
	Description: "entities and evidenced relations extracted from the source text",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"entities": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        map[string]any{"type": "string"},
						"type":        map[string]any{"type": "string"},
						"description": map[string]any{"type": "string"},
					},
					"required": []string{"name", "type", "description"},
				},
			},
			"relations": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"source":     map[string]any{"type": "string"},
						"predicate":  map[string]any{"type": "string"},
						"target":     map[string]any{"type": "string"},
						"evidence":   map[string]any{"type": "string"},
						"confidence": map[string]any{"type": "number"},
					},
					"required": []string{"source", "predicate", "target", "evidence", "confidence"},
				},
			},
		},
		"required": []string{"entities", "relations"},
	},
}

// ExtractTriples extracts one document's graph via provider-native structured
// output.
//
// It receives a summary whose Summary is ALREADY body-only — the pass owns
// frontmatter handling, so this function never strips. It does no
// normalization, no persistence and no config defaulting, and it returns its
// error honestly; ExtractTriplesPass is the layer that contains failures.
func ExtractTriples(
	ctx context.Context,
	summary SummaryResult,
	tcfg config.TriplesConfig,
	model string,
	validTypes, validPredicates []string,
	client *llm.Client,
) (ExtractedGraph, error) {
	var graph ExtractedGraph

	// Neutralize spoofed delimiter tags before the summary joins the template's
	// untrusted frame. Render does NOT neutralize — the call site must, exactly
	// as concepts.go does for the same reason (SEC-04, second-order injection).
	// Applied to the path too, for consistency with buildSourceContext: a
	// filename may legally contain the opening tag on Linux.
	prompt, err := prompts.Render("extract_triples", prompts.TriplesData{
		ValidTypes:      strings.Join(validTypes, ", "),
		ValidPredicates: strings.Join(validPredicates, ", "),
		SourcePath:      prompts.NeutralizeTags(summary.SourcePath),
		Summary:         prompts.NeutralizeTags(summary.Summary),
	}, "")
	if err != nil {
		return graph, fmt.Errorf("render extract_triples: %w", err)
	}

	payload, _, err := client.StructuredCompletion(ctx, []llm.Message{
		{Role: "system", Content: "You are a knowledge-graph extraction system. Output valid JSON only."},
		{Role: "user", Content: prompt},
	}, TriplesSchema, llm.CallOpts{Model: model, MaxTokens: tcfg.MaxTokens})
	if err != nil {
		return graph, err
	}

	if err := json.Unmarshal(payload, &graph); err != nil {
		return graph, fmt.Errorf("parse extracted graph: %w", err)
	}
	return graph, nil
}

// confidenceFloor is the minimum confidence an LLM-extracted edge carries.
//
// A model that returns 0 would produce an edge that can never win AddRelation's
// confidence-guarded upsert against the Pass-3 keyword extractor, which asserts
// the same edges with confidence 0 — so the evidence this pass exists to record
// would be silently dropped. The floor is deliberately low: it orders an
// unscored LLM edge above a keyword edge and below every scored one.
const confidenceFloor = 0.1

// normalizeGraph enforces every constraint the JSON schema deliberately does
// not (see TriplesSchema): the configured entity-type and predicate
// vocabularies, endpoint integrity, and the per-document caps.
//
// The ORDER is load-bearing, not stylistic:
//   - entity truncation runs BEFORE endpoint validation, because a relation
//     pointing at a truncated entity fails its foreign key at AddRelation and
//     is lost to a log line;
//   - the zero-relation sweep runs LAST, because which entities are stranded is
//     only known after every edge rule has run.
//
// Every drop is counted and warned, never silent.
func normalizeGraph(
	g ExtractedGraph,
	ont store.OntologyStore,
	defs []ontology.RelationDef,
	maxEntities, maxRelations int,
) ExtractedGraph {
	// 1-2. Trim, drop nameless, coerce unknown types. IsValidType is the single
	// oracle — the same one WriteArticles uses — because AddEntity validates
	// against the store's set, so any other source can coerce to a type the
	// store then rejects.
	var entities []ExtractedEntity
	coerced, unnamed := 0, 0
	for _, e := range g.Entities {
		e.Name = strings.TrimSpace(e.Name)
		if e.Name == "" {
			unnamed++
			continue
		}
		e.Type = strings.TrimSpace(e.Type)
		if e.Type == "" || (ont != nil && !ont.IsValidType(e.Type)) {
			e.Type = ontology.TypeConcept
			coerced++
		}
		e.Description = strings.TrimSpace(e.Description)
		entities = append(entities, e)
	}
	if unnamed > 0 {
		log.Warn("triples: dropped entities with no name", "count", unnamed)
	}
	if coerced > 0 {
		log.Warn("triples: coerced entities to concept (type not in configured set)", "count", coerced)
	}

	// 3. Truncate entities before anything validates against the set.
	if maxEntities > 0 && len(entities) > maxEntities {
		log.Warn("triples: entity cap reached, dropping the tail",
			"kept", maxEntities, "dropped", len(entities)-maxEntities)
		entities = entities[:maxEntities]
	}
	surviving := make(map[string]bool, len(entities))
	for _, e := range entities {
		surviving[e.Name] = true
	}

	// 4-8. Predicate mapping, endpoint integrity, self-loops, confidence.
	var relations []ExtractedRelation
	unmapped, dangling, selfLoops := 0, 0, 0
	for _, r := range g.Relations {
		pred, ok := mapPredicate(strings.TrimSpace(r.Predicate), defs)
		if !ok {
			unmapped++
			continue
		}
		r.Predicate = pred
		r.Source = strings.TrimSpace(r.Source)
		r.Target = strings.TrimSpace(r.Target)

		if r.Source == r.Target {
			selfLoops++
			continue
		}
		if !surviving[r.Source] || !surviving[r.Target] {
			dangling++
			continue
		}

		switch {
		case r.Confidence > 1:
			r.Confidence = 1
		case r.Confidence < confidenceFloor:
			r.Confidence = confidenceFloor
		}
		r.Evidence = strings.TrimSpace(r.Evidence)
		relations = append(relations, r)
	}
	if unmapped > 0 {
		// Upstream prefers "warn, not drop", but every pipeline path builds the
		// ontology store with a non-nil valid-relation set and AddRelation
		// rejects anything outside it — so the warning carries the signal and
		// the edge cannot.
		log.Warn("triples: dropped relations with an unmapped predicate", "count", unmapped)
	}
	if dangling > 0 {
		log.Warn("triples: dropped relations with an endpoint outside the extracted set", "count", dangling)
	}
	if selfLoops > 0 {
		log.Warn("triples: dropped self-referential relations", "count", selfLoops)
	}

	// 9. Truncate relations.
	if maxRelations > 0 && len(relations) > maxRelations {
		log.Warn("triples: relation cap reached, dropping the tail",
			"kept", maxRelations, "dropped", len(relations)-maxRelations)
		relations = relations[:maxRelations]
	}

	// 10. Drop entities no surviving relation touches.
	connected := make(map[string]bool, len(relations)*2)
	for _, r := range relations {
		connected[r.Source] = true
		connected[r.Target] = true
	}
	kept := entities[:0]
	for _, e := range entities {
		if connected[e.Name] {
			kept = append(kept, e)
		}
	}
	if stranded := len(entities) - len(kept); stranded > 0 {
		log.Warn("triples: dropped entities left with no relations", "count", stranded)
	}

	return ExtractedGraph{Entities: kept, Relations: relations}
}

// mapPredicate resolves a free-text predicate onto the configured vocabulary:
// exact name first, then a case-insensitive synonym match. Returns false when
// neither matches.
func mapPredicate(pred string, defs []ontology.RelationDef) (string, bool) {
	if pred == "" {
		return "", false
	}
	lower := strings.ToLower(pred)
	for _, d := range defs {
		if strings.EqualFold(d.Name, pred) {
			return d.Name, true
		}
	}
	for _, d := range defs {
		for _, syn := range d.Synonyms {
			if strings.ToLower(syn) == lower {
				return d.Name, true
			}
		}
	}
	return "", false
}

// tripleRelationID derives a collision-free id for an extracted edge.
//
// Deliberately NOT write.go's `source + "-" + relation + "-" + target`, which is
// not injective: with lowercase-hyphenated names, ("a-b", extends, "c") and
// ("a", "b-extends", "c") both render "a-b-extends-c". Two distinct triples
// would then collide on the PRIMARY KEY — a conflict AddRelation's
// ON CONFLICT(source_id, target_id, relation) target does not cover, so the
// insert errors and the edge is dropped at the caller's log line.
//
// A separator-joined string would only move the assumption (nothing enforces
// that model output lacks the separator); hashing NUL-separated fields needs no
// assumption at all. Nothing in the repo parses or reconstructs a relation id —
// the only consumer is the primary key. The "triple:" prefix additionally
// guarantees no collision with a keyword edge's id.
func tripleRelationID(source, predicate, target string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + predicate + "\x00" + target))
	return "triple:" + hex.EncodeToString(sum[:8])
}

// persistGraph writes a normalized graph: entities first (foreign-key order),
// then evidenced relations.
//
// Entity-type precedence, in order:
//  1. the row already exists  -> keep its stored type. The pass is a
//     high-volume writer of unvalidated model output and must never overwrite a
//     type an article declared. Revalidated on the way through, because a stale
//     custom type would make AddEntity reject the write and silently lose the
//     description.
//  2. the name is one of THIS run's concepts -> use the derivation Pass 3 uses
//     (write.go), so both passes assert the same type for the row they share.
//  3. otherwise -> the normalized model type. Nothing else claims these.
//
// Persistence is sequential by contract: GetEntity reads outside the write
// mutex, so concurrent callers would both observe "absent" and race on the
// type. ExtractTriplesPass fans out extraction and joins before calling this.
func persistGraph(ont store.OntologyStore, g ExtractedGraph, concepts []ExtractedConcept, sourceDoc string) error {
	conceptTypes := make(map[string]string, len(concepts))
	for _, c := range concepts {
		t := c.Type
		if t == "" || !ont.IsValidType(t) {
			t = ontology.TypeConcept
		}
		conceptTypes[c.Name] = t
	}

	for _, e := range g.Entities {
		resolved := e.Type
		if existing, err := ont.GetEntity(e.Name); err == nil && existing != nil {
			resolved = existing.Type
			if resolved == "" || !ont.IsValidType(resolved) {
				resolved = ontology.TypeConcept
			}
		} else if t, ok := conceptTypes[e.Name]; ok {
			resolved = t
		}

		// No ArticlePath: Pass 3 supplies it for entities that become articles,
		// and AddEntity's empty-field guard is what lets both survive.
		if err := ont.AddEntity(store.Entity{
			ID:         e.Name,
			Type:       resolved,
			Name:       ontology.FormatConceptName(e.Name),
			Definition: e.Description,
		}); err != nil {
			log.Warn("triples: entity write failed", "entity", e.Name, "error", err)
		}
	}

	for _, r := range g.Relations {
		if err := ont.AddRelation(store.Relation{
			ID:         tripleRelationID(r.Source, r.Predicate, r.Target),
			SourceID:   r.Source,
			TargetID:   r.Target,
			Relation:   r.Predicate,
			Evidence:   r.Evidence,
			Confidence: r.Confidence,
			SourceDoc:  sourceDoc,
		}); err != nil {
			log.Warn("triples: relation write failed",
				"source", r.Source, "predicate", r.Predicate, "target", r.Target, "error", err)
		}
	}
	return nil
}

// Per-document caps and fan-out defaults. Applied in-function because
// config.Defaults() has no Ontology entry and is only reached through
// config.Load — a Config{} literal (routine in this package's tests, and the
// shape a zero-valued TriplesConfig arrives in) yields zeros, and a zero cap
// would truncate the graph to nothing after paying for the call.
const (
	defaultTriplesMaxTokens          = 4096
	defaultTriplesMaxEntitiesPerDoc  = 40
	defaultTriplesMaxRelationsPerDoc = 60
)

// ExtractTriplesPass runs LLM triple extraction over a Pass-2 summary set and
// persists the result (P3-2). It is opt-in via ontology.triples.enabled.
//
// It NEVER returns an error and never fails a compile: this is an additive
// enrichment pass, and an extraction outage must not stop articles from being
// written. That is the deliberate inverse of ExtractConcepts, whose output
// gates article writing — so no existing caller's error branch changes.
// ExtractTriples itself does return errors; this wrapper is the only layer that
// swallows, and it counts and logs everything it swallows.
//
// Extraction fans out; normalization and persistence run sequentially after the
// join. Both stores serialize writes, so concurrent writes would be safe — but
// GetEntity reads OUTSIDE the write mutex, so two documents extracting the same
// new entity concurrently would both observe "absent" and race on its type.
func ExtractTriplesPass(
	ctx context.Context,
	ont store.OntologyStore,
	summaries []SummaryResult,
	concepts []ExtractedConcept,
	cfg *config.Config,
	client *llm.Client,
	summariesCarryFrontmatter bool,
) {
	if cfg == nil || !cfg.Ontology.Triples.Enabled || ont == nil || client == nil || len(summaries) == 0 {
		return
	}
	// opts.Ctx is nilable at the fullpipeline call site; a per-document
	// ctx.Err() on a nil interface panics.
	if ctx == nil {
		ctx = context.Background()
	}

	defer metrics.ObserveDuration(metrics.HistogramNamed(
		"compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "triples"), time.Now())

	tcfg := cfg.Ontology.Triples
	if tcfg.MaxTokens <= 0 {
		tcfg.MaxTokens = defaultTriplesMaxTokens
	}
	if tcfg.MaxEntitiesPerDoc <= 0 {
		tcfg.MaxEntitiesPerDoc = defaultTriplesMaxEntitiesPerDoc
	}
	if tcfg.MaxRelationsPerDoc <= 0 {
		tcfg.MaxRelationsPerDoc = defaultTriplesMaxRelationsPerDoc
	}
	// Floored at 1: make(chan struct{}, 0) is unbuffered, and the only receiver
	// is each goroutine's own deferred release — every send blocks forever and
	// wg.Wait never returns. ExtractConcepts is safe only because of its own
	// guard; this pass must carry it, not inherit it by proximity.
	concurrency := cfg.Compiler.MaxParallel
	if concurrency <= 1 {
		concurrency = 1
	}

	model := tcfg.Model
	if model == "" {
		model = cfg.Models.Extract
		if model == "" {
			model = cfg.Models.Summarize
			if model == "" {
				model = "gpt-4o-mini"
			}
		}
	}

	defs := ontology.MergedRelations(cfg.Ontology.Relations)
	validPredicates := ontology.ValidRelationNames(defs)
	validTypes := ontology.ValidEntityTypeNames(ontology.MergedEntityTypes(cfg.Ontology.EntityTypes))

	// Cost attribution. Set before the fan-out and restored after the join:
	// c.pass is unsynchronized and trackUsage reads it from request goroutines.
	prior := client.Pass()
	client.SetPass("triples")
	defer client.SetPass(prior)

	type extraction struct {
		graph     ExtractedGraph
		sourceDoc string
		ok        bool
	}
	results := make([]extraction, len(summaries))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	var mu sync.Mutex
	failures := 0
	cancelled := false

	for i, s := range summaries {
		wg.Add(1)
		go func(i int, s SummaryResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Checked after acquiring a slot too: a cancel while queued must not
			// start a paid call.
			if ctx.Err() != nil {
				mu.Lock()
				cancelled = true
				mu.Unlock()
				return
			}

			body, sourceDoc := resolveSourceDoc(s, summariesCarryFrontmatter)
			doc := s
			doc.Summary = body

			graph, err := ExtractTriples(ctx, doc, tcfg, model, validTypes, validPredicates, client)
			if err != nil {
				// Every early exit either records a failure or fills its slot.
				// A cancel mid-flight surfaces as an error here; classify it as
				// cancellation, not as a per-document failure.
				mu.Lock()
				if ctx.Err() != nil {
					cancelled = true
				} else {
					failures++
					log.Warn("triples: extraction failed", "source", s.SourcePath, "error", err)
				}
				mu.Unlock()
				return
			}
			results[i] = extraction{graph: graph, sourceDoc: sourceDoc, ok: true}
		}(i, s)
	}
	wg.Wait()

	if cancelled {
		log.Warn("triples: extraction cancelled", "documents", len(summaries))
		return
	}

	entities, relations := 0, 0
	for _, r := range results {
		if !r.ok {
			continue
		}
		g := normalizeGraph(r.graph, ont, defs, tcfg.MaxEntitiesPerDoc, tcfg.MaxRelationsPerDoc)
		if len(g.Entities) == 0 && len(g.Relations) == 0 {
			continue
		}
		if err := persistGraph(ont, g, concepts, r.sourceDoc); err != nil {
			log.Warn("triples: persist failed", "source", r.sourceDoc, "error", err)
			continue
		}
		entities += len(g.Entities)
		relations += len(g.Relations)
	}

	if failures > 0 {
		log.Warn("triples: some documents failed extraction",
			"failed", failures, "of", len(summaries))
	}
	log.Info("triples extracted", "entities", entities, "relations", relations, "documents", len(summaries))
}

// resolveSourceDoc returns the summary body and the document the triples came
// from.
//
// carriesFrontmatter is set true ONLY at the re-extract call site, the one
// producer that puts the summary FILE (frontmatter included) into
// SummaryResult.Summary — where SourcePath is the summary's filename, not the
// source document. Sniffing for a leading `---` instead would misfire: on the
// normal path a summary body may legitimately open with a horizontal rule.
//
// Both frontmatter shapes parse: summarize.go's five-key form and the batch
// path's three-key form. The body is returned stripped, so an evidence span can
// never be quoted out of `compiled_at:`.
func resolveSourceDoc(s SummaryResult, carriesFrontmatter bool) (body, sourceDoc string) {
	if !carriesFrontmatter {
		return s.Summary, s.SourcePath
	}
	rest, ok := strings.CutPrefix(s.Summary, "---\n")
	if !ok {
		return s.Summary, s.SourcePath
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return s.Summary, s.SourcePath
	}
	front, remainder := rest[:end], rest[end:]
	body = strings.TrimPrefix(remainder, "\n---")
	body = strings.TrimPrefix(body, "\n")

	sourceDoc = s.SourcePath
	for _, line := range strings.Split(front, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "source:"); ok {
			if v = strings.Trim(strings.TrimSpace(v), `"'`); v != "" {
				sourceDoc = v
			}
			break
		}
	}
	return body, sourceDoc
}
