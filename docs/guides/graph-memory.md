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

### What it does not do yet

- **Entity resolution.** "NASA" and "National Aeronautics and Space
  Administration" remain separate nodes. Until that lands, surface-form variants
  fracture the graph.
- **Temporal validity.** The three temporal columns are stored and returned but
  nothing reads them; contradicting facts collide rather than invalidating each
  other.
- **Graph query.** There is no multi-hop question-answering tool over these
  edges yet.
