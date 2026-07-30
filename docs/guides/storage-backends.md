# Storage Backends

sage-wiki stores all persistent state through a backend seam
(`internal/store`). Two backends exist:

| Backend | Use | CGO |
|---|---|---|
| `sqlite` (default) | Zero-config single-file vault (`.sage/wiki.db`) | none |
| `postgres` | Multi-user server mode, pgvector ANN search | none |

Everything above the seam — CLI, MCP, TUI, web, compile, re-embed, query,
reconcile — opens storage through the same factory and store interfaces, so
all of them honor `storage.backend` — including the startup reconcile
(P3-7), which heals whichever backend is configured. On Postgres, if another
writer holds the vault's advisory lock (e.g. a running `serve`), the startup
reconcile stalls up to `storage.lock_timeout` (default 5s) then skips with a
warning instead of blocking startup. (The `status` command's no-shared-stores
fallback and `wiki init`'s bootstrap remain sqlite-only; see notes in
`decisions.md` for those two residual paths.)

## Choosing a backend

```yaml
# config.yaml
storage:
  backend: postgres            # sqlite (default) | postgres
  dsn: ${SAGE_WIKI_PG_DSN}     # env-expanded; never commit credentials
  vector_dimension: 768        # required: must match your embedding model
  lock_timeout: "5s"           # writer-lock acquisition timeout
  pool:
    max_open: 10
    max_idle: 2
```

With no `storage:` section, behavior is byte-identical to previous releases
(sqlite, same file, same pragmas, same migrations).

## Postgres prerequisites

1. PostgreSQL 14+ with the **pgvector extension installed**:
   `CREATE EXTENSION vector;` (requires superuser or rds_superuser).
   Without it, writer open fails with an actionable error naming this step.
2. A database per vault: `CREATE DATABASE my_vault;`
3. `storage.dsn` pointing at it, `storage.vector_dimension` matching your
   embedding model's output size.

On first writer open, sage-wiki runs its own migration set (V1: tables,
`sage_fts` text-search configuration, HNSW vector indexes) inside that
database. Migrations are append-only; readers never migrate — a reader
whose schema is behind fails with "run any writer command once."

**Upgrade note (schema v6-v7).** v6 rebuilds the generated `entries.tsv`
column so Postgres weights id/article_path above body content (`setweight`
A/B/D), matching SQLite's BM25 column weights; v7 adds the `entry_dates`
sidecar behind the recency signal. The v6 column rebuild rewrites the
`entries` table under an `ACCESS EXCLUSIVE` lock — on a large vault, run the
first writer command in a maintenance window, since readers fail with "run any
writer command once" until it finishes.

## Switching an existing vault to postgres

The database is a rebuildable index; **files are the truth**. There is no
SQLite→Postgres data migration:

1. Configure `storage.backend: postgres` + DSN + dimension.
2. Run any writer command once (creates the schema).
3. `sage-wiki compile --fresh` (rebuilds the index from source files,
   including re-embedding — bring an embedding provider).

Search ranking on postgres uses `ts_rank` over a snowball stemmer instead of
FTS5 BM25 — result *sets* are equivalent, exact scores/order may differ
slightly. Stopword-only queries fall back to substring matching; CJK recall
is comparably limited on both backends.

## Reader and writer modes

- **Writer** (default; CLI, MCP, TUI, web, compile): runs migrations, takes
  a session advisory lock so **one writer process owns a vault** — a second
  writer fails fast at open naming the remedy (including the crashed-writer
  case: inspect `pg_locks`, `pg_terminate_backend` the stale session).
  Every write transaction serializes through an xact advisory lock with
  `lock_timeout` matching SQLite's 5s `busy_timeout` behavior.
- **Reader** (hub federated search): no lock, no migrations, MVCC reads.
  Reader pools are sized 4/2 per project so hub fan-out across many projects
  can't exhaust `max_connections`. Note: config validation requires
  `vector_dimension` for postgres; a reader opened without it (bypassing
  config) gets empty vector-search results by design (dimension guard).

### Pool sizing

The pinned advisory-lock connection consumes one `max_open` slot
permanently; a long re-embed consumes another transiently. Effective pool is
`max_open − 1` (and `−2` during re-embed). The default 10 is comfortable;
below 4 is not recommended for writer processes.

## Changing the embedding model / vector dimension

`storage.vector_dimension` is validated against the actual columns at open.
On mismatch, open fails with `ErrDimensionMismatch`. Remedy: drop and
recreate the vector tables, invalidate `output_index` (forces reconcile),
then re-embed — the same flow a model change requires on sqlite.

## Concurrency: what postgres does NOT change (yet)

Although postgres removes the single-writer file constraint, sage-wiki
deliberately keeps single-writer-process semantics in this release: the
write mutex, the advisory locks, and the compile coordinator are unchanged.
Durable compile workers (P2-3) have since shipped — the serve-mode worker
with leases, heartbeats, and a dead-letter queue is configured under
`serve.worker`; see [configuration.md](configuration.md#serveworker).
Multi-writer postgres concurrency (`SELECT … FOR UPDATE` hardening)
remains out of scope.

## Implementation map

| Piece | Where |
|---|---|
| Interfaces + types + sentinels | `internal/store` |
| Factory | `internal/storedial` |
| SQLite backend | `internal/sqlitestore` (+ concrete stores in `memory`, `vectors`, …) |
| Postgres backend | `internal/storage/postgres` |
| Conformance suite | `internal/storetest` (sqlite always; postgres under `TEST_DATABASE_URL`) |
