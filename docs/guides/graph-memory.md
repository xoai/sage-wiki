# Graph memory

Sage Wiki's ontology is a typed graph: entities (concepts, techniques, sources,
claims, artifacts) joined by typed relations. This guide covers the two pieces
that turn it from a label graph into a graph you can cite: **evidenced
relations** and **LLM triple extraction**.

## Evidenced relations

Every relation can carry:

| Field | Meaning |
|---|---|
| `evidence` | the text span supporting the edge |
| `confidence` | 0–1, how strongly the source supports it |
| `source_doc` | the document the edge came from |
| `valid_from`, `valid_to`, `invalidated_by` | reserved for temporal validity; stored but not yet used |

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

**It proposes; by default it does not apply.** `auto_apply_threshold` defaults to
`1.0`, which means never auto-apply, so every proposal waits for you. See
"Review-only by default" below.

It **links; it does not collapse.** Both entity rows survive, and the canonical
gains the alias's edges — so traversal from the canonical sees the whole cluster
while nothing is ever deleted. `sage-wiki ontology list` still shows both rows,
and traversal from the alias still shows only its own edges. Collapsing into a
single node needs alias-aware prune, reconcile and manifest handling first, and
is deliberately left to a later cycle.

**And it is reversible.** A derived edge is stored separately from the ones your
sources actually asserted, stamped with the link that caused it, so
`--unlink` removes exactly those and nothing else. That separation is the whole
reason the undo is exact rather than a guess — see "Undoing a link" below.

### Review-only by default

Enabling resolution links nothing on its own:

```yaml
ontology:
  resolve:
    enabled: true
    # auto_apply_threshold defaults to 1.0 — never auto-apply
```

Every proposal is queued for you to decide. This default predates `--unlink` —
it was introduced when a link genuinely could not be taken back. Now that one
can, review-only is a *conservative* default rather than a necessary one: a
mistaken link costs an `--unlink`, not a permanently fractured graph. Lower the
threshold if that trade suits your vault.

So that the queue is not invisible, the pass warns at the normal log level —
no `-v` needed — whenever proposals are standing, on every exit path and in
every configuration:

```
WARN resolve: entity-resolution proposals are waiting for review —
     run `sage-wiki ontology resolve --review` to decide them  pending=12
```

### Opting in to automatic linking

Lower the threshold. `0.85` was the previous default and is a reasonable
starting point:

```yaml
ontology:
  triples:
    enabled: true
  resolve:
    enabled: true
    auto_apply_threshold: 0.85
```

`triples` is in that block because once you have lowered the threshold,
auto-apply *also* requires a description on at least one side of the pair, and
triple extraction is the only **compile-path** writer of entity descriptions.

That pairing is not itself a guarantee — `sage-wiki scribe` writes descriptions
outside the compile path, so a scribe-described entity can auto-link against a
bare Pass-3 concept even with `triples: false`. The threshold is what guarantees
review-only; the description is a second condition on top of it. The pass warns
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
   and, below `auto_apply_threshold: 1.0`, re-apply it — so a delete on its own
   is a pause, not an undo.
3. **Links are re-derived.** Under a chain A→B→C, edges that reached C came from
   A *via* B and are recorded against B, so removing A's own rows is not enough.
   Rebuilding from the links that survive is what clears them.

A proposal goes to review rather than applying automatically when
`auto_apply_threshold` is at its default of `1.0` (which forbids auto-apply
outright), when its confidence is below a lowered threshold, when the model flags
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
- **Un-linking.** The alias table records every link, so one can be undone by
  hand, but there is no single command for it yet.
- **Alias-aware search.** A query for "Buzz" does not yet find canonical
  "Buzz Aldrin" — that is graph-query work.
- **Temporal validity.** The three temporal columns are stored and returned but
  nothing reads them; contradicting facts collide rather than invalidating each
  other.
- **Graph query.** There is no multi-hop question-answering tool over these
  edges yet.
