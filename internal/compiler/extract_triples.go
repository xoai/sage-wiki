package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/sourcedate"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/pkg/events"
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
	pr *prompts.Registry,
	temp *float64,
) (ExtractedGraph, error) {
	var graph ExtractedGraph

	// The source path is folded INTO the untrusted payload, not rendered beside
	// it: a path in the prompt's trusted region lets a file named to carry
	// instructions steer this pass, and NeutralizeTags defangs only the
	// delimiters, not prose. concepts.go:154 does the same for the same reason
	// (SEC-04, second-order injection). One NeutralizeTags over the whole
	// payload — path included, since a filename may legally contain the opening
	// tag on Linux — because Render does not neutralize.
	body := prompts.NeutralizeTags(fmt.Sprintf("### Source: %s\n%s", summary.SourcePath, summary.Summary))
	prompt, err := renderPrompt(pr, "extract_triples", prompts.TriplesData{
		ValidTypes:      strings.Join(validTypes, ", "),
		ValidPredicates: strings.Join(validPredicates, ", "),
		Summary:         body,
	}, "")
	if err != nil {
		return graph, fmt.Errorf("render extract_triples: %w", err)
	}

	payload, _, err := client.StructuredCompletion(ctx, []llm.Message{
		{Role: "system", Content: "You are a knowledge-graph extraction system. Output valid JSON only."},
		{Role: "user", Content: prompt},
	}, TriplesSchema, llm.CallOpts{Model: model, MaxTokens: tcfg.MaxTokens, Temperature: temp})
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

		// Self-loop before endpoint check (spec §5.4 lists them the other way).
		// Behaviourally identical — both drop the edge — this only decides
		// which counter an edge that is both increments.
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
// FunctionalSupersession records one functional-predicate edge asserted this
// compile so the post-resolution sweep can re-fire InvalidateFunctional once
// this run's alias links exist (P3-6 two-trigger design: persistGraph runs
// strictly BEFORE ResolveEntitiesPass, so alias forms created by THIS run's
// resolution are invisible to the write-time trigger).
type FunctionalSupersession struct {
	SourceID      string
	Predicate     string
	KeepTargetID  string
	NewValidFrom  string
	InvalidatedBy string
}

// temporalHooks carries the write-time supersession context into persistGraph
// (P3-6 T6b). trust is nilable: conflicts are skipped with a debug log.
type temporalHooks struct {
	enabled    bool
	functional map[string]bool
	threshold  float64
	trust      store.TrustStore
	projectDir string
}

// emitEdgeConflict records a trust conflict for a contradiction the pipeline
// could not auto-resolve (P3-6): below-threshold functional clashes and bare
// entity-level contradicts edges (which carry no deterministic mapping to the
// contradicted triple, so auto-invalidation would be guesswork). Dedup is by
// deterministic ID: a recompile of the same doc re-generates the same ID and
// the Get short-circuits; the residual race between concurrent MCP callers
// loses to the PK constraint and is swallowed at debug — never surfaced.
func emitEdgeConflict(h temporalHooks, sourceDoc, question, answer string) {
	if h.trust == nil {
		log.Debug("triples: edge conflict (no trust store wired)", "question", question)
		return
	}
	sum := sha256.Sum256([]byte(question))
	id := "edgeconflict-" + hex.EncodeToString(sum[:])[:16]
	if existing, err := h.trust.Get(id); err == nil && existing != nil {
		return
	}
	sourcesUsed, _ := json.Marshal([]string{sourceDoc})
	o := &store.PendingOutput{
		ID:           id,
		Question:     question,
		QuestionHash: trust.HashQuestion(question),
		Answer:       answer,
		AnswerHash:   trust.HashAnswer(answer),
		State:        store.StateConflict,
		SourcesUsed:  string(sourcesUsed),
		SourcesHash:  trust.ComputeSourcesHash(h.projectDir, string(sourcesUsed)),
		CreatedAt:    config.NowUTC(),
	}
	if err := h.trust.InsertPending(o); err != nil {
		// Duplicate under a concurrent writer, or a store failure: either way
		// the conflict surfacing is best-effort and must never fail a compile.
		log.Debug("triples: edge conflict insert skipped", "id", id, "error", err)
	}
}

// runSupersessionSweep is the second trigger of the P3-6 two-trigger design:
// it re-fires InvalidateFunctional for this compile's functional edges AFTER
// ResolveEntitiesPass. The write-time trigger runs before resolution, so an
// alias form created or applied by THIS run is invisible to its form-set
// expansion — and the resolution replay would re-derive a live copy of the
// superseded edge. InvalidateFunctional only touches not-yet-invalidated
// rows, so re-firing is a no-op wherever the first trigger sufficed.
func runSupersessionSweep(ont store.OntologyStore, supersessions []FunctionalSupersession) {
	for _, sup := range supersessions {
		if _, err := ont.InvalidateFunctional(sup.SourceID, sup.Predicate,
			sup.KeepTargetID, sup.NewValidFrom, sup.InvalidatedBy); err != nil {
			log.Warn("triples: post-resolution sweep failed", "source", sup.SourceID,
				"predicate", sup.Predicate, "error", err)
		}
	}
}

// validFromForDoc resolves when the facts in sourceDoc became true (P3-6):
// frontmatter date → file mtime → manifest added-at, via sourcedate.Resolve.
// Zero means unknown and stays EMPTY — writing the epoch would date every
// undated fact 1970. sourceDoc is project-relative on both call paths
// (resolveSourceDoc strips to the source: key or the relative SourcePath).
func validFromForDoc(projectDir string, mf *manifest.Manifest, sourceDoc string) string {
	abs := sourceDoc
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectDir, sourceDoc)
	}
	addedAt := ""
	if mf != nil {
		if src, ok := mf.Sources[sourceDoc]; ok {
			addedAt = src.AddedAt
		}
	}
	ts := sourcedate.Resolve(abs, addedAt)
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

// Persistence is sequential by contract: GetEntity reads outside the write
// mutex, so concurrent callers would both observe "absent" and race on the
// type. ExtractTriplesPass fans out extraction and joins before calling this.
func persistGraph(ont store.OntologyStore, g ExtractedGraph, concepts []ExtractedConcept, sourceDoc, validFrom string, hooks temporalHooks) (entities, relations int, persisted []string, supersessions []FunctionalSupersession) {
	conceptTypes := make(map[string]string, len(concepts))
	for _, c := range concepts {
		t := c.Type
		if t == "" || !ont.IsValidType(t) {
			t = ontology.TypeConcept
		}
		conceptTypes[c.Name] = t
	}

	// Entities that did not land: their edges would fail the foreign key with a
	// message that gives no hint the endpoint was the cause.
	skipped := map[string]bool{}

	for _, e := range g.Entities {
		// A lookup ERROR is not "absent". Falling through to rules 2/3 on a
		// transient read failure (a busy database, a concurrent reader) would
		// write the model's guessed type over a type an article declared —
		// exactly what rule 1 exists to prevent. Skip the entity instead.
		existing, err := ont.GetEntity(e.Name)
		if err != nil {
			log.Warn("triples: entity type lookup failed, skipping", "entity", e.Name, "error", err)
			skipped[e.Name] = true
			continue
		}

		resolved := e.Type
		if existing != nil {
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
			skipped[e.Name] = true
			continue
		}
		entities++
		// The id this pass actually LANDED, for P3-3's touched set. Attempted
		// writes are deliberately excluded: resolution must not arbitrate over
		// an entity that is not in the graph.
		persisted = append(persisted, e.Name)
	}

	for _, r := range g.Relations {
		if skipped[r.Source] || skipped[r.Target] {
			log.Warn("triples: relation skipped — an endpoint entity did not persist",
				"source", r.Source, "predicate", r.Predicate, "target", r.Target)
			continue
		}
		edgeID := tripleRelationID(r.Source, r.Predicate, r.Target)
		if err := ont.AddRelation(store.Relation{
			ID:         edgeID,
			SourceID:   r.Source,
			TargetID:   r.Target,
			Relation:   r.Predicate,
			Evidence:   r.Evidence,
			Confidence: r.Confidence,
			SourceDoc:  sourceDoc,
			ValidFrom:  validFrom,
		}); err != nil {
			log.Warn("triples: relation write failed",
				"source", r.Source, "predicate", r.Predicate, "target", r.Target, "error", err)
			continue
		}
		relations++

		if !hooks.enabled {
			continue
		}
		// Bare contradicts edges never auto-invalidate: an entity-level
		// A --contradicts--> B carries no deterministic mapping to the
		// contradicted triple (spec rev 2 scope decision).
		if r.Predicate == ontology.RelContradicts {
			emitEdgeConflict(hooks, sourceDoc,
				fmt.Sprintf("Edge conflict: %s contradicts %s (source: %s)", r.Source, r.Target, sourceDoc),
				"Deferred: entity-level contradicts edge recorded for review; no auto-invalidation.")
			continue
		}
		if !hooks.functional[r.Predicate] {
			continue
		}
		if r.Confidence >= hooks.threshold {
			vf := validFrom
			if vf == "" {
				vf = config.NowUTC().Format(time.RFC3339)
			}
			if _, err := ont.InvalidateFunctional(r.Source, r.Predicate, r.Target, vf, edgeID); err != nil {
				// Best-effort like every write in this pass: the compile must
				// not fail over supersession. The post-resolution sweep
				// re-fires idempotently.
				log.Warn("triples: supersession failed", "source", r.Source,
					"predicate", r.Predicate, "error", err)
			}
			supersessions = append(supersessions, FunctionalSupersession{
				SourceID:      r.Source,
				Predicate:     r.Predicate,
				KeepTargetID:  r.Target,
				NewValidFrom:  vf,
				InvalidatedBy: edgeID,
			})
		} else {
			var current []string
			if rels, err := ont.GetRelations(r.Source, store.Outbound, r.Predicate); err == nil {
				for _, cr := range rels {
					if cr.TargetID != r.Target {
						current = append(current, cr.TargetID)
					}
				}
			}
			// Sorted: the conflict text keys the deterministic dedup ID, and
			// GetRelations has no ORDER BY on SQLite — an unstable list would
			// mint a new conflict row per compile (Gate-3 review).
			sort.Strings(current)
			emitEdgeConflict(hooks, sourceDoc,
				fmt.Sprintf("Edge conflict: %s %s %v vs %s (source: %s)",
					r.Source, r.Predicate, current, r.Target, sourceDoc),
				fmt.Sprintf("Deferred: confidence %.2f below auto-apply threshold %.2f; both values kept live.",
					r.Confidence, hooks.threshold))
		}
	}
	return entities, relations, persisted, supersessions
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
// tripleTimeout records a doc whose per-doc budget expired during the triples
// pass, carrying the typed error so the caller can emit the AC11 event pair.
type tripleTimeout struct {
	SourcePath string
	Err        error
}

func ExtractTriplesPass(
	ctx context.Context,
	ont store.OntologyStore,
	summaries []SummaryResult,
	concepts []ExtractedConcept,
	cfg *config.Config,
	client *llm.Client,
	summariesCarryFrontmatter bool,
	projectDir string,
	mf *manifest.Manifest,
	trustStore store.TrustStore,
	pr *prompts.Registry,
	budgets *DocBudgets,
	sink events.Sink,
) (touched []string, supersessions []FunctionalSupersession, timeouts []tripleTimeout) {
	if cfg == nil || !cfg.Ontology.Triples.Enabled || ont == nil || client == nil || len(summaries) == 0 {
		return nil, nil, nil
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

	hooks := temporalHooks{
		enabled:    cfg.Ontology.Temporal.EnabledOrDefault(),
		functional: map[string]bool{},
		threshold:  cfg.Ontology.Temporal.AutoApplyThresholdOrDefault(),
		trust:      trustStore,
		projectDir: projectDir,
	}
	// config.Load normalizes relation_types into Relations — one loop only.
	for _, rc := range cfg.Ontology.Relations {
		if rc.Functional {
			hooks.functional[rc.Name] = true
		}
	}

	// Cost attribution. Set before the fan-out and restored after the join:
	// c.pass is unsynchronized and trackUsage reads it from request goroutines.
	prior := client.Pass()
	client.SetPass("triples")
	defer client.SetPass(prior)

	type extraction struct {
		graph     ExtractedGraph
		sourceDoc string
		// body is the resolved source text the triples were extracted from
		// (resolveSourceDoc output) — the grounding surface for SPEC-08
		// span verification.
		body string
		ok   bool
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

			// SPEC-08 D6: the per-doc budget (shared with the doc's Pass-1
			// units) bounds this unit; queueing never consumes it.
			unitCtx := ctx
			var budget *DocBudget
			var cancelUnit context.CancelFunc
			if budgets != nil {
				budget = budgets.For(s.SourcePath)
				if budget.Expired() {
					// SPEC-08 AC11: a per-doc budget expiry is a typed timeout.
					// The caller emits the event pair and marks the run
					// incomplete so the doc is retried (its summary persisted,
					// so only an incomplete run re-enters it).
					mu.Lock()
					timeouts = append(timeouts, tripleTimeout{SourcePath: s.SourcePath, Err: docTimeoutError(budget)})
					mu.Unlock()
					log.Warn("triples skipped: per-doc timeout budget exhausted", "source", s.SourcePath)
					return
				}
				unitCtx, cancelUnit = budget.UnitContext(ctx)
			}

			body, sourceDoc := resolveSourceDoc(s, summariesCarryFrontmatter)
			// The re-extract path loads every .md in summaries/ with no
			// validity check, unlike ExtractConcepts and filterSuccessful — so
			// a frontmatter-only file would arrive here as an empty body and
			// buy a paid call that can only return an empty graph.
			if strings.TrimSpace(body) == "" {
				if cancelUnit != nil {
					cancelUnit()
				}
				return
			}
			doc := s
			doc.Summary = body

			start := time.Now()
			graph, err := ExtractTriples(unitCtx, doc, tcfg, model, validTypes, validPredicates, client, pr, cfg.Compiler.CompileTemperature())
			budgetTimedOut := false
			if budget != nil {
				budgetTimedOut = budget.finishUnit(unitCtx, time.Since(start), err)
				cancelUnit()
			}
			if err != nil {
				// Every early exit either records a failure or fills its slot.
				// A cancel mid-flight surfaces as an error here; classify it as
				// cancellation, not as a per-document failure. A per-doc budget
				// expiry is a typed timeout failure (SPEC-08 AC11).
				mu.Lock()
				switch {
				case budgetTimedOut:
					timeoutErr := docTimeoutError(budget)
					timeouts = append(timeouts, tripleTimeout{SourcePath: s.SourcePath, Err: timeoutErr})
					log.Warn("triples: per-doc timeout", "source", s.SourcePath, "error", timeoutErr)
				case ctx.Err() != nil:
					cancelled = true
				default:
					failures++
					log.Warn("triples: extraction failed", "source", s.SourcePath, "error", err)
				}
				mu.Unlock()
				return
			}
			results[i] = extraction{graph: graph, sourceDoc: sourceDoc, body: doc.Summary, ok: true}
		}(i, s)
	}
	wg.Wait()

	// A cancel does not discard graphs that already came back. They are complete
	// and validated, and AddRelation's upsert makes re-assertion idempotent —
	// unlike the manifest/compile_items checkpoint, which an incomplete run must
	// not touch. Note what this does and does not buy: the next compile still
	// re-summarizes and re-extracts these documents (the item store was not
	// advanced), so the spend is not saved. What is saved is having the graph
	// available in the interim instead of throwing away finished work.
	if cancelled {
		completed := 0
		for _, r := range results {
			if r.ok {
				completed++
			}
		}
		log.Warn("triples: extraction cancelled", "documents", len(summaries), "completed", completed)
	}

	entities, relations := 0, 0
	var persisted []string
	for _, r := range results {
		if !r.ok {
			continue
		}
		g := normalizeGraph(r.graph, ont, defs, tcfg.MaxEntitiesPerDoc, tcfg.MaxRelationsPerDoc)
		// SPEC-08 D4/AC3 span verification: compile output is DATA — an edge
		// whose evidence span does not actually appear in the resolved source
		// text is dropped (with edge_rejected + metric) before any persist.
		g = verifyEvidenceSpans(g, r.body, sink, filepath.Base(projectDir))
		if len(g.Entities) == 0 && len(g.Relations) == 0 {
			continue
		}
		// Counts are what actually landed, not what was attempted: a failed
		// write must not be reported as an extracted entity.
		e, rel, ids, sup := persistGraph(ont, g, concepts, r.sourceDoc, validFromForDoc(projectDir, mf, r.sourceDoc), hooks)
		entities += e
		relations += rel
		persisted = append(persisted, ids...)
		supersessions = append(supersessions, sup...)
	}

	if failures > 0 {
		log.Warn("triples: some documents failed extraction",
			"failed", failures, "of", len(summaries))
	}
	log.Info("triples extracted", "entities", entities, "relations", relations, "documents", len(summaries))
	return dedupeIDs(persisted), supersessions, timeouts
}

// dedupeIDs collapses the per-document id lists into one stable set. The same
// entity is routinely extracted from several documents in a run.
func dedupeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
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
	// Normalize line endings first: a summary written or edited on Windows has
	// "---\r\n", which a \n-only prefix match misses entirely.
	summary := strings.ReplaceAll(s.Summary, "\r\n", "\n")
	rest, ok := strings.CutPrefix(summary, "---\n")
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

// normalizeForSpanMatch lowercase-folds and collapses all whitespace runs so
// a span quoted with different spacing/case still grounds (SPEC-08 D4).
func normalizeForSpanMatch(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// evidenceGrounded reports whether the evidence span actually appears in the
// resolved source text. Empty evidence never grounds (an empty string is a
// substring of everything — it would launder fabricated edges).
func evidenceGrounded(evidence, sourceText string) bool {
	ev := normalizeForSpanMatch(evidence)
	if ev == "" {
		return false
	}
	return strings.Contains(normalizeForSpanMatch(sourceText), ev)
}

// verifyEvidenceSpans drops relations whose Evidence is not present in the
// resolved source text (SPEC-08 D4/AC3 — compile outputs are data; schema
// validation alone cannot stop a fabricated evidence span). Each dropped
// edge emits edge_rejected and increments edge_rejected_total
// {reason:"span_missing"}. Entities are kept — other grounded edges or the
// article pass may still use them.
func verifyEvidenceSpans(g ExtractedGraph, sourceText string, sink events.Sink, wsName string) ExtractedGraph {
	if len(g.Relations) == 0 {
		return g
	}
	kept := make([]ExtractedRelation, 0, len(g.Relations))
	for _, rel := range g.Relations {
		if evidenceGrounded(rel.Evidence, sourceText) {
			kept = append(kept, rel)
			continue
		}
		metrics.CounterNamed("edge_rejected_total", "reason", "span_missing").Inc()
		if sink != nil {
			sink.Emit(events.NewEvent(wsName, events.TypeEdgeRejected, events.EdgeRejected{
				Source:    rel.Source,
				Predicate: rel.Predicate,
				Target:    rel.Target,
				Reason:    "span_missing",
			}))
		}
		log.Warn("triples: edge rejected — evidence span not in source",
			"source", rel.Source, "predicate", rel.Predicate, "target", rel.Target)
	}
	g.Relations = kept
	return g
}
