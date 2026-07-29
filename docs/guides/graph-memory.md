# Graph memory

Sage Wiki's ontology is a typed graph: entities (concepts, techniques, sources,
claims, artifacts) joined by typed relations. This guide covers the two pieces
that turn it from a label graph into a graph you can cite: **evidenced
relations** and **LLM triple extraction**.

The graph also feeds retrieval directly: it is the **third channel of the
fused ranking** on every search surface — query terms seed entities, a bounded
traversal ranks their neighborhoods, and those results fuse with the lexical
and vector channels at `search.hybrid_weight_graph`. Per-relation traversal
weights (`search.graph_relation_weights`) and how to ablate the channel per
call live in [search-quality.md](search-quality.md); an empty ontology costs
nothing and leaves results byte-identical.

## Evidenced relations

Every relation can carry:

| Field | Meaning |
|---|---|
| `evidence` | the text span supporting the edge |
| `confidence` | 0–1, how strongly the source supports it |
| `source_doc` | the document the edge came from |
| `valid_from`, `valid_to`, `invalidated_by` | bi-temporal validity window and the edge that superseded this one (see "Temporal validity" below) |

These are additive. Relations created before this feature — and relations
created today by keyword extraction — simply have them empty, and every existing
query behaves exactly as before.

### Where an evidence span comes from

**`evidence` is quoted from the document's compiled summary, not from the raw
source file.** Triple extraction runs in Pass 2, which operates on summaries;
the raw source has already been parsed and discarded by then.

So for an edge with `source_doc: raw/paper.pdf`, the span will be found in
`wiki/summaries/paper.md`, not in `paper.pdf`. `source_doc` answers "where did
this knowledge come from"; the summary is what the quote is verifiable against.
Feeding raw sources to Pass 2 instead would mean re-parsing every PDF a second
time per compile, which is the most expensive thing the compiler does.

### Re-assertion

When the same `(source, target, relation)` edge is asserted again, its evidence
is updated **only if the new confidence is strictly higher**. The edge keeps its
original `created_at`.

This matters because two passes assert edges. Keyword extraction (Pass 3) runs
on every compile and asserts with no confidence; LLM extraction asserts with a
model-supplied score. The guard is what stops a keyword re-run from erasing an
LLM edge's evidence.

## LLM triple extraction

Off by default. Turn it on per project:

```yaml
ontology:
  triples:
    enabled: true
    # model: ""                    # default: models.extract, then models.summarize
    # max_tokens: 4096
    # max_entities_per_doc: 40
    # max_relations_per_doc: 60
```

With it on, each Tier-3 document gets one additional LLM call during Pass 2. The
model returns entities (with a one-sentence grounded description each) and
evidenced triples between them, which are persisted as ontology entities and
relations.

### What it costs

**One LLM call per Tier-3 document, per compile that processes it.** Use the
cheap model — the default chain resolves to `models.extract`, then
`models.summarize`.

Two things to know before enabling it on a large vault:

- **`--re-extract` is O(all summaries), not O(changed).** That command reloads
  every summary on disk, so with triples enabled it makes one call per article
  in the whole wiki, every run. On a 2,000-article wiki that is 2,000 calls.
- **The `--batch` compile path does not run triple extraction.** Batch users get
  triples on their next standard compile or `--re-extract`. (The batch path's
  summary set is not tier-filtered, so running a paid pass there would extract
  from Tier-1 and Tier-2 sources too.)

Set `max_entities_per_doc` / `max_relations_per_doc` lower to cap what a single
verbose document can add. Anything over the cap is dropped with a warning naming
the count — never silently.

### What it does with imperfect model output

The extraction schema deliberately does **not** constrain entity types or
predicates to an enum, even though both come from your configured vocabulary.
A schema violation fails the entire call, so one hallucinated predicate would
cost the document's whole graph. Instead the vocabulary is enforced afterwards,
where a bad value costs exactly one node or edge:

- an unknown entity type becomes `concept`;
- a predicate that matches no relation type or synonym drops that edge;
- a relation pointing at an entity that was not extracted drops;
- self-referential relations drop;
- an entity left with no relations drops, so it does not become a lint orphan.

Every one of these is counted and logged.

### Interaction with keyword extraction

Keyword extraction still runs, unchanged, in Pass 3. One visible consequence of
enabling triples: keyword extraction gates each pattern on the *stored* entity
types, and it skips a pattern whose target entity does not exist yet. Once
triple extraction populates typed entities in Pass 2, some keyword edges that
were previously skipped will start being created.

## Entity resolution

`ontology.resolve.enabled: true` adds a pass that finds surface-form variants of
one entity — "NASA" and "National Aeronautics and Space Administration" — which
would otherwise stay two disconnected nodes with half the graph each.

**It proposes and, by default, applies only the certain ones.**
`auto_apply_threshold` defaults to `0.85`: a proposal at or above it that also
passes every guard is linked automatically, and any mistake is one `--unlink`
away. Set an explicit `1.0` to keep every proposal waiting for you — see
"Review-only as an opt-in" below.

It **links; it does not collapse.** Both entity rows survive, and the canonical
gains the alias's edges — so traversal from the canonical sees the whole cluster
while nothing is ever deleted. `sage-wiki ontology list` still shows both rows,
and at the *store* level an alias still keeps only its own stored edges — the
conformance suite pins that on both backends. The user surfaces sit above the
store and resolve first: graph-expansion seeds in query/search, the query
context's fallback traversal, MCP `wiki_ontology_query`, the web graph's
`?center=`, and `ontology query --entity` all start from the canonical, so
asking any of them about the alias shows the whole cluster. Collapsing into a
single node needs alias-aware prune, reconcile and manifest handling first, and
is deliberately left to a later cycle.

**And it is reversible.** A derived edge is stored separately from the ones your
sources actually asserted, stamped with the link that caused it, so
`--unlink` removes exactly those and nothing else. That separation is the whole
reason the undo is exact rather than a guess — see "Undoing a link" below.

### Review-only as an opt-in

Set the threshold to exactly `1.0` and resolution links nothing on its own:

```yaml
ontology:
  resolve:
    enabled: true
    auto_apply_threshold: 1.0   # never auto-apply — every proposal queues
```

That `1.0` is a hard guarantee, not just a high bar: the pass treats it as
*never*, by an explicit branch, so even a model reporting confidence `1.0`
cannot clear it. Review-only was the default while a link could not be taken
back; `--unlink` changed the economics — a mistaken link now costs one command,
not a permanently fractured graph — so `0.85` became the default and the queue
became the opt-in.

So that the queue is not invisible, the pass warns at the normal log level —
no `-v` needed — whenever proposals are standing, on every exit path and in
every configuration:

```
WARN resolve: entity-resolution proposals are waiting for review —
     run `sage-wiki ontology resolve --review` to decide them  pending=12
```

### Automatic linking (the default)

`auto_apply_threshold` defaults to `0.85`, so an enabled pass auto-applies out
of the box; state the value explicitly if you want it pinned against future
default changes:

```yaml
ontology:
  triples:
    enabled: true
  resolve:
    enabled: true
    auto_apply_threshold: 0.85   # the default, stated
```

`triples` is in that block because auto-apply *also* requires a description on
at least one side of the pair, and triple extraction is the only
**compile-path** writer of entity descriptions.

That pairing is not itself a guarantee — `sage-wiki scribe` writes descriptions
outside the compile path, so a scribe-described entity can auto-link against a
bare Pass-3 concept even with `triples: false`. An explicit `1.0` threshold is
what guarantees review-only; the description is a second condition on top of
the threshold, not a substitute for it. The pass warns
once per run when it sees `resolve` without `triples`, because that combination
usually means descriptions will be missing.

### Reviewing and deciding

```
sage-wiki ontology resolve --review              # list pending proposals
sage-wiki ontology resolve --apply  <alias-id>   # apply one
sage-wiki ontology resolve --reject <alias-id>   # reject; never re-proposed
sage-wiki ontology resolve --unlink <alias-id>   # undo an APPLIED link
sage-wiki ontology resolve --sweep               # re-derive approved links (free)
```

### Undoing a link

```
sage-wiki ontology resolve --unlink "NASA"
✓ unlinked NASA → National Aeronautics and Space Administration (pair rejected; 3 links re-derived)
```

Three things happen, and all three are necessary:

1. **The edges that link caused are deleted** — exactly those, because each
   derived edge records which link produced it. Edges your sources asserted
   directly are untouched.
2. **The pair is rejected.** Without this the next compile would re-propose it
   and — at the default `auto_apply_threshold` — re-apply it, so a delete on
   its own is a pause, not an undo.
3. **Links are re-derived.** Under a chain A→B→C, edges that reached C came from
   A *via* B and are recorded against B, so removing A's own rows is not enough.
   Rebuilding from the links that survive is what clears them.

A proposal goes to review rather than applying automatically when
`auto_apply_threshold` is an explicit `1.0` (which forbids auto-apply
outright), when its confidence is below the threshold, when the model flags
one member as a strictly *broader* concept than the others, or when neither side
has a description.
Rejection is symmetric: rejecting A→B also blocks B→A, so re-rolling the
direction cannot bypass your decision.

### What it costs

Only entities the current compile touched seed an arbitration call, and a new
entity that matches nothing costs **zero** calls. Name tokens shared by more
than `max_token_df` of a type are ignored for blocking, so a common word like
"model" does not drag hundreds of entities into one call — with a floor
(`min_token_df_floor`) so a genuinely rare name in a small vault is not
discarded along with it.

`use_embeddings: true` additionally catches names sharing no tokens at all
("NYC" / "New York City"). It is off by default because the embedding API has no
batch endpoint here: every vector is one HTTP call, vectors are held in memory
for the pass and discarded, and each enabled compile re-embeds up to
`max_embed_candidates` entities.

### Things worth knowing

- **A derived edge cites the alias's document.** Its `evidence` span and
  `source_doc` are the alias's, carried over unchanged, so an edge on the
  canonical may quote a document that never names the canonical. Derived edges
  live in their own table, stamped with the link that produced them, which is
  what lets `--unlink` remove exactly those.
- **A derived edge never overwrites the canonical's own assertion.** If the
  canonical already asserts the same edge, its evidence and confidence are kept.
- **The compile-path sweep only runs when there is something to compile.** Edges
  added by reconcile, the MCP tools, trust promotion or scribe reach the
  canonical on the next compile, or immediately via
  `sage-wiki ontology resolve --sweep`.
- **Pruning an alias does not immediately remove what it derived.** A derived
  edge references the *canonical* and the far endpoint, not the alias, so
  deleting the alias entity leaves the canonical still showing the edge. The
  next sweep notices the missing endpoint and rebuilds without it. Deleting the
  canonical or the far endpoint does remove them at once, by cascade. To
  separate two entities without deleting either, use `--unlink`.
- **`source`-type entities are never resolved.** Their identity is their file
  path, and two documents with the same basename would otherwise look identical.

### What it does not do yet

- **Collapsing into one node.** Resolution links; both rows remain.
- **Re-asserting an invalidated edge.** Supersession is one-way; reviving a
  fact is a deliberate future operation, not a side effect of re-compilation.

(Un-linking, alias-aware seeding, multi-hop graph query, and temporal
validity — once on this list — now exist: `ontology resolve --unlink`,
canonical seed resolution at every user surface, `wiki_graph_query` below,
and the bi-temporal edges of the next section.)

## Communities and global queries

Local graph queries answer "what relates to X?". **Global** queries answer
"what are the main themes across everything?" — query-focused summarization,
not retrieval. With `ontology.communities.enabled: true`, each compile runs a
deterministic pure-Go Louvain detection over the live entity graph
(hierarchical levels), generates a cached summary per community of 3+
members, and re-summarizes only communities whose membership changed.

Ask globally with `wiki_graph_query` and `mode: "global"`: the answer
map-reduces over relevant community summaries and cites the communities it
used. Community markdown lands in `wiki/communities/` alongside concepts.

**Cost honesty:** detection itself is free (in-memory, milliseconds at
personal-vault scale). Summaries cost one cheap-model call per community of
3+ members on first enable (roughly entities/10), then only for changed
communities. A global query costs 1 + K calls (map over up to
`max_communities` communities, then reduce). This is why it is off by
default — enable it when global questions matter more than the indexing cost.

## Temporal validity

Edges are bi-temporal: `valid_from` records when a fact became true (the
source document's frontmatter date, else file mtime, else when it was first
compiled) and `valid_to` when it stopped (empty = still true). Default reads
return only currently-valid edges, so a corrected fact does not collide with
its predecessor — the old edge is invalidated (`valid_to` set,
`invalidated_by` pointing at the winner), never deleted, and point-in-time
questions remain answerable:

- `wiki_graph_query` accepts an optional `as_of` (RFC3339) — "what did we
  believe in January?"
- `ontology query --as-of <RFC3339>` traverses only edges valid then.

Supersession is driven by **functional predicates** — relations with at most
one live target per source. Mark them in config:

```yaml
ontology:
  temporal:
    enabled: true               # default
    auto_apply_threshold: 0.8   # default
  relations:
    - name: works_at
      functional: true          # OUTBOUND uniqueness: one live employer per person
```

`functional` means outbound uniqueness ("each source has at most one live
target"); edges are stored only as asserted, so an inbound-unique relation
(employs) should be expressed as its outbound form instead. A new edge on a
functional predicate invalidates the previous one when its confidence meets
`auto_apply_threshold`; below the threshold both stay live and a reviewable
conflict appears in `trust outputs list --state conflict`. Entity-level
`contradicts` edges always surface a conflict rather than auto-invalidating —
an A-contradicts-B edge does not identify which specific fact it disputes.

## Asking the graph

The `wiki_graph_query` MCP tool answers relational questions by traversal
rather than article retrieval:

- **Args:** `question` (required), `hops` (1–5, default 2), `max_edges`
  (1–500, default 60). Defaults come from `ontology.graph_query`
  (`max_hops`, `max_edges`); out-of-range values — config or per-call — fall
  back rather than clamping.
- **How it answers:** seed entities are resolved from the question via
  hybrid search (an alias seed lands on its canonical entity), a bounded
  subgraph is serialized as numbered triples —
  `E3: (Buzz Aldrin) --[pilots]--> (Apollo 11) {source: raw/a.md, confidence: 0.90}`
  — and the model must answer ONLY from those edges.
- **Citations:** the response returns the cited edges with their
  `source_doc` and `confidence` (and evidence span when present). An answer
  the edges cannot support says so and cites nothing.
- **Bounds are honest:** when the edge cap truncates the subgraph the
  response says `truncated: true`; nothing pretends to have read the whole
  graph. Zero matched entities or zero edges return a distinct answer with
  no LLM call.
- **Provenance in regular queries too:** the Q&A context's related-article
  fallback names the connecting edge under each `### Related:` header
  (`via: (a) --[extends]--> (b) {source: raw/a.md, confidence: 0.80}`).
  Graph-EXPANDED articles are deliberately not annotated — expansion
  aggregates many signals, and naming a single edge there would be false
  provenance.
