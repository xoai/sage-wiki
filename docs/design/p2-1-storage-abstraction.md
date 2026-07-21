# Design: P2-1 — Storage abstraction + optional Postgres/pgvector backend

**Status:** draft (first commit of PR per Phase-2 spec preamble)
**Spec:** `.sage/docs/sage-wiki-upgrade/06-spec-phase2-strategic.md` §P2-1
**Cycle:** `.sage/work/20260721-p2-1-storage-abstraction/`

## 1. Problem

All persistent state lives in one SQLite file behind `storage.DB`, with ~75
exported store methods across 8 concrete store types, plus raw-SQL escape hatches
in `web`, `mcp`, `linter`, `wiki/status`, and `compiler/reembed`. There is no
backend seam: swapping storage requires touching every consumer. We want an
optional Postgres backend (multi-user server mode, pgvector ANN) with SQLite
unchanged as the zero-config default.

## 2. Survey findings that shape the design

- Every store shares one `*storage.DB` (write handle + 4-conn read pool).
- Cross-store writes use caller-owned `*sql.Tx` in 4 places:
  `wiki/reconcile.go` applyReindex, `compiler/write.go`, `mcp/tools_write.go`,
  `trust/consensus.go` (package-level fns joining `pending_questions_vec` ⋈
  `pending_outputs`).
- `vectors.Store` carries two in-memory matrix caches with a mandatory
  `InvalidateChunkCache()` post-commit contract (3 call sites).
- FTS5 is SQLite-specific (`MATCH`, `rank`, `prefix='2 3'`, `"term"*"` builder
  in `memory/entries.go`).
- SQLite-isms throughout: `?` placeholders, `INSERT OR IGNORE/REPLACE`,
  `datetime('now')` defaults, table-rebuild migrations.
- `compile_items` is a state-table queue with no claim semantics — ports cleanly.
- DB path is hard-coded convention `<projectDir>/.sage/wiki.db` at ~7 sites; no
  `storage:` config section exists.

## 3. Design decisions

### D1 — `database/sql` stays the portability substrate

Postgres backend uses `pgx/v5` **stdlib driver** (`github.com/jackc/pgx/v5/stdlib`),
pure Go, `CGO_ENABLED=0` preserved. Interface signatures may reference `*sql.Tx`
without leaking backend specifics. A future pgx-native backend is explicitly out
of scope. *Alternative rejected:* pgx native pool — better performance, but forces
`pgx.Tx` into every cross-store signature and breaks the stdlib seam for zero
current benefit.

### D2 — Interface decomposition mirrors today's stores

One interface per existing store (Go-small interfaces, consumer-side):

```
EntryStore, ChunkStore, VectorStore, OntologyStore,
TrustStore, CompileItemStore, OutputIndexStore
```

Plus a `Backend` aggregate: constructor + the seven accessors + `Close()`.
`storage.DB` keeps its `WriteTx(fn)` primitive as the cross-store atomicity
mechanism; the Postgres backend exposes the identical `WriteTx` shape so the 4
caller-owned-tx sites port mechanically. Tx-scoped store methods
(`IndexChunks(tx,…)`, `UpsertChunk(tx,…)`, `SetOutputHashTx`, trust consensus fns)
keep `*sql.Tx` parameters (justified by D1).

*Alternative rejected:* unit-of-work redesign (TxStores aggregate) — cleaner but a
behavior-risking rewrite of the reconciler/compiler write paths; the spec mandates
seam-first with zero behavior change.

### D3 — Raw-SQL escape hatches move behind store methods

No consumer outside the store packages touches `ReadDB()`/`WriteDB()` after the
seam. Concrete moves:

| Site | Raw SQL today | New home |
|---|---|---|
| `web/server.go` graph stats, relations dump | `ReadDB()` queries | `OntologyStore` methods |
| `mcp/server.go:301` stats query | `ReadDB()` | `CompileItemStore`/`OntologyStore` |
| `linter/learning.go`, `linter/passes.go` | `learnings` table | new minimal `LearningStore` |
| `wiki/status.go` | status queries | `CompileItemStore` |
| `compiler/reembed.go` | `vec_entries` scans | `VectorStore` |

*Rationale:* an escape hatch that only works on SQLite makes the Postgres backend
silently feature-broken. Cost is real (interface surface grows ~8 methods) but it
is the only way "same test suite against both backends" is meaningful.

### D4 — Vector cache invalidation stays an interface method

`InvalidateChunkCache()` remains on `VectorStore`; the Postgres backend
implements it as a no-op (search is DB-side via pgvector). Redesigning callers to
route all chunk writes through self-committing store methods is a bigger
behavioral change than the seam justifies. Documented as a wart to retire in P2-3.

### D5 — FTS dialect split per backend, no generic SQL abstraction layer

Each interface gets two implementations; SQL text lives next to each. SQLite keeps
FTS5 exactly as-is. Postgres uses generated `tsvector` columns +
`websearch_to_tsquery` + `ts_rank`, prefix terms via `:*`. Ranking parity is
**approximate** — conformance tests assert result-set properties (right doc in
top-k, filters applied), never exact scores or order beyond relevance sanity.
*Alternative rejected:* placeholder-rebind + query-builder layer (sqlx/squirrel) —
adds a dependency and obscures dialect differences that are semantic (FTS), not
syntactic.

### D6 — Postgres vectors via pgvector, dimension from config

`vec_entries`/`vec_chunks` map to `vector(N)` columns; `N` comes from new
`storage.vector_dimension` config (required when backend=postgres; the embedding
client already knows its dimension — validated at open against the column).
Search: `<=>` cosine distance, optional HNSW index created when row count crosses
a threshold (documented, not auto-tuned in this task). Dimension mismatch →
explicit error at open, never silent truncation.

### D7 — Migrations: Postgres gets its own embedded set

`internal/storage/postgres/migrations/*.sql` via `embed`, same `schema_version`
pattern, append-only, **one statement per Exec** (pgx stdlib rejects
multi-statement prepared calls — known gotcha). SQLite's V1–V8 series untouched.
The SQLite table-rebuild pattern (V2/V4/V8) does not translate; the Postgres set
is written fresh at current schema shape and starts at V1.

### D8 — Config

```yaml
storage:
  backend: sqlite            # sqlite | postgres (default sqlite)
  dsn: ${SAGE_WIKI_PG_DSN}   # env expansion; secrets never literal in file
  vector_dimension: 768      # required for postgres
  pool: { max_open: 10, max_idle: 2 }
```

Threaded through `app.Open`; the ~7 hard-coded path sites keep the SQLite
convention when backend=sqlite (zero behavior change) and reject postgres where a
site is path-only (hub multi-project: postgres projects addressed by DSN from
each project's config).

### D9 — Concurrency model documented, unchanged

SQLite path: single-writer `WriteTx` mutex exactly as today. Postgres removes the
single-writer constraint, but **compile parallelism is unchanged in this task** —
`CompileCoordinator` still serializes; P2-3 (durable job model) owns exploiting
Postgres concurrency. Docs state this explicitly so no one assumes otherwise.

### D10 — Files-as-truth invariant preserved

The DB remains a rebuildable index on Postgres too: the P1-2 reconciler works
against both backends through the interfaces; `output_index` (drift comparand)
exists in both schemas. Postgres bootstrap for an existing vault = open empty DB
+ reconcile/recompile, never SQLite→Postgres data migration.

## 4. Test strategy

1. **Seam phase:** full existing suite green + P1-8 golden-output checks where
   user-visible text is near (error prefixes byte-compared).
2. **Conformance suite:** shared test package driven by a backend factory; runs
   against SQLite unconditionally and Postgres when `TEST_DATABASE_URL` is set
   (testcontainers or external DSN). Covers every interface method incl. tx
   flows, cache-invalidation contract, and error cases.
3. **Boot path:** local container run before merge (env-gated CI means boot SQL
   is otherwise unexercised — recorded learning).

## 5. Sequencing (feeds plan.md)

1. Config `storage:` section + `app.Open` threading (no behavior change).
2. Interface extraction + escape-hatch moves (zero behavior change; SQLite impls
   are today's code relocated).
3. Conformance suite against SQLite.
4. Postgres backend + pgvector + migrations; conformance matrix green.
5. Docs (`docs/storage-backends.md`), config scaffolding, CHANGELOG.

## 6. Explicit non-goals

Compile parallelism changes, pgx-native protocol, property-graph backends,
SQLite→Postgres data migration, exact FTS ranking parity, retiring the
cache-invalidation wart.
