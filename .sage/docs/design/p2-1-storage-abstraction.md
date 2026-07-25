# Design: P2-1 — Storage abstraction + optional Postgres/pgvector backend

**Status:** draft, review iteration 6

> Iteration log: i1 2C/6M/5S/2cos · i2 0C/2M/4S/1cos · i3 2C/1M/3S/1cos ·
> i4 0C/1M/6S · i5 1C/0M/1S/1cos — shared advisory key made a writer's own
> WriteTx self-conflict with its session lock (dead on arrival); two distinct
> keys derived in D9.2. See cycle decisions.md.

## 1. Problem

All persistent state lives in one SQLite file behind `storage.DB`, with ~75
exported store methods across 8 concrete store types, plus raw-SQL escape hatches
in `web`, `mcp`, `linter`, `wiki/status`, and `compiler/reembed`. There is no
backend seam: swapping storage requires touching every consumer. We want an
optional Postgres backend (multi-user server mode, pgvector ANN) with SQLite
unchanged as the zero-config default.

## 2. Survey findings that shape the design (audited)

- Every store shares one `*storage.DB` (write handle + 4-conn read pool).
- Caller-owned write paths crossing store boundaries (audited — 9 sites):
  `wiki/reconcile.go` applyReindex, `compiler/write.go`, `mcp/tools_write.go`,
  `trust/consensus.go`, `query/query.go:699`, `trust/promote.go` (:107/:155/:172),
  `trust/hooks.go:69`, `compiler/index.go:213`, `compiler/backfill.go:97`.
- `compiler/reembed.go:108` uses raw `WriteDB().Begin()` — bypasses the write
  mutex and holds the single write connection across network embedding calls.
  Pre-existing behavior; the seam must reproduce it, not fix it (see D9).
- `vectors.Store` carries two in-memory matrix caches with a mandatory
  `InvalidateChunkCache()` post-commit contract (3 call sites).
- **Three** vector tables exist: `vec_entries`, `vec_chunks`, and
  `pending_questions_vec` (`trust/consensus.go:15-77`, joined against
  `pending_outputs`; per-row dimension-mismatch skip at consensus.go:49).
- FTS5 is SQLite-specific (`MATCH`, `rank`, `prefix='2 3'`); `buildFTSQuery`
  (`memory/entries.go:172-195`) OR-joins `"term"*` prefix terms.
- SQLite-isms throughout: `?` placeholders, `INSERT OR IGNORE/REPLACE`,
  `datetime('now')` TEXT defaults, table-rebuild migrations.
- `compile_items` is a state-table queue with no claim semantics — ports cleanly.
- DB path convention `<projectDir>/.sage/wiki.db` is hard-coded at **~12 sites**
  (audited): `app.go`, `tui/compile/model.go`, `pipeline.go` ×2, `reembed.go`,
  `reextract.go`, `wiki/init.go` ×2, `wiki/doctor.go`, `wiki/status.go:93`,
  `hub/search.go`, `mcp/tools_compound.go` (passes a raw `DBPath` string
  downstream). No `storage:` config section exists.
- `wiki/status.go:211` probes `sqlite_master` (no Postgres equivalent);
  `wiki/doctor.go` `os.Stat`s the DB file (no Postgres meaning).

## 3. Design decisions

### D1 — `database/sql` stays the portability substrate; pgvector types registered

Postgres backend uses `pgx/v5` **stdlib driver** (pure Go, `CGO_ENABLED=0`
preserved) **plus `github.com/pgvector/pgvector-go`**: vector columns are
read/written as `pgvector.Vector` (a `[]float32` wrapper with sql
Scanner/Valuer), registered via `pgxvector.RegisterTypes` in the stdlib driver's
`AfterConnect` hook. **The open mechanism is pinned:** the pool must be created
via `stdlib.OpenDB(*pgx.ConnConfig)` with `ConnConfig.AfterConnect` calling
`pgxvector.RegisterTypes` — a plain `sql.Open("pgx", dsn)` silently never fires
the hook and fails at first vector I/O (the classic pgx stdlib trap). Without
registration the stdlib driver does not know the `vector` OID and every vector
read/write fails at runtime. This is the one new runtime
dependency; it is justified: there is no CGO-free alternative for typed pgvector
I/O, and a text-format fallback (`'[1,2,3]'::vector` casts + manual parse) was
rejected as slower and more error-prone. Interface signatures may reference
`*sql.Tx` without leaking backend specifics (both backends are database/sql
drivers). A future pgx-native backend is explicitly out of scope.

### D2 — Interface decomposition mirrors today's stores

One interface per existing store (Go-small interfaces, consumer-side):

```
EntryStore, ChunkStore, VectorStore, OntologyStore,
TrustStore, CompileItemStore, OutputIndexStore, LearningStore
```

`LearningStore` is new and minimal — it wraps the `learnings` table access
currently done with raw SQL in `linter/learning.go` and `linter/passes.go`
(whose `:249` read of `vec_entries` moves to `VectorStore.Get` instead).

A `Backend` aggregate: constructor + the **eight** accessors + `Close()` +
capability methods for the non-store probes (D3).

`storage.DB`'s `WriteTx(fn)` remains the cross-store atomicity primitive; the
Postgres backend exposes the identical `WriteTx` shape so the 9 audited
caller-owned-tx sites port mechanically. Tx-scoped methods keep `*sql.Tx`
parameters (justified by D1).

*Alternative rejected:* unit-of-work redesign (TxStores aggregate) — cleaner but
a behavior-risking rewrite of the reconciler/compiler write paths; the spec
mandates seam-first with zero behavior change.

### D3 — Raw-SQL escape hatches move behind store/backend methods (complete list)

No consumer outside the store packages touches `ReadDB()`/`WriteDB()` after the
seam. Audited moves:

| Site | Raw SQL today | New home |
|---|---|---|
| `web/server.go` graph stats, relations dump | `ReadDB()` queries | `OntologyStore` methods |
| `mcp/server.go:301` stats query | `ReadDB()` | `CompileItemStore`/`OntologyStore` |
| `linter/learning.go`, `linter/passes.go` | `learnings` table | `LearningStore` |
| `linter/passes.go:249` | `vec_entries` direct read | `VectorStore.Get` |
| `wiki/status.go` | status queries | `CompileItemStore` |
| `wiki/status.go:211` | `sqlite_master` probe | `Backend.SchemaReady() bool` |
| `wiki/doctor.go` | `os.Stat` DB existence | `Backend.Health()` (sqlite: file checks; postgres: `Ping` + version) |
| `compiler/reembed.go` | `vec_entries` scans | `VectorStore` |
| `mcp/tools_compound.go:68` | raw `DBPath` string downstream | `Backend.Location() string` (path, or host/db extracted from DSN — never credentials) |

*Rationale:* an escape hatch that only works on SQLite makes the Postgres backend
silently feature-broken. Cost is real (interface surface grows ~10 methods) but
it is the only way "same test suite against both backends" is meaningful.

### D4 — Vector cache invalidation stays an interface method

`InvalidateChunkCache()` remains on `VectorStore`; the Postgres backend
implements it as a no-op (search is DB-side via pgvector). Redesigning callers to
route all chunk writes through self-committing store methods is a bigger
behavioral change than the seam justifies. Documented as a wart to retire in P2-3.

### D5 — FTS dialect split per backend; prefix and config semantics pinned

Each interface gets two implementations; SQL text lives next to each. SQLite
keeps FTS5 exactly as-is. Postgres:

- Generated `tsvector` columns with the **`simple` text-search config** (not
  `english`): FTS5's unicode61 tokenizer does not stem, and `simple` does not
  either — stemming under `english` would diverge ranking/recall from SQLite.
- Query translation reimplements `buildFTSQuery` semantics directly:
  `websearch_to_tsquery` is **not** used (it cannot emit `:*` prefix operators
  and ANDs unquoted terms, while `buildFTSQuery` OR-joins `"term"*`); the
  Postgres query builder OR-joins `sanitized:*` terms in `to_tsquery('simple', …)`
  to mirror it.
- Ranking: `ts_rank` vs FTS5 `rank` — **approximate parity only**. Conformance
  tests assert result-set properties (expected doc in top-k, filters applied),
  never exact scores.
- CJK: neither FTS5/unicode61 nor Postgres `simple` segments CJK into
  searchable tokens (both treat contiguous ideographs as one token), so CJK
  recall is comparably poor on both backends. Conformance suite includes a CJK
  fixture asserting **parity of behavior** (same doc set returned), not recall
  quality. Improving CJK search is out of scope for both backends here.
- Implementation checklist: verify `:*` prefix queries actually use the
  tsvector GIN index (`EXPLAIN` on a populated table); if they seq-scan,
  large-vault search latency diverges from FTS5's `prefix='2 3'` index even
  when result sets match — mitigation (trigram index or query rewrite) would
  be decided then, not speculated here.

*Alternative rejected:* placeholder-rebind + query-builder layer (sqlx/squirrel) —
adds a dependency and obscures dialect differences that are semantic (FTS), not
syntactic.

### D6 — Postgres vectors via pgvector; all three vector tables; dimension from config

`vec_entries`, `vec_chunks`, **and `pending_questions_vec`** map to `vector(N)`
columns; `N` comes from new `storage.vector_dimension` config (required when
backend=postgres; validated at open against the column). Search: `<=>` cosine
distance. An HNSW index is created **in the bootstrap migration** (tables are
empty at bootstrap, so plain `CREATE INDEX` inside the migration tx is safe — no
`CONCURRENTLY` needed; no runtime auto-tuning in this task).
`CREATE EXTENSION vector` is a documented bootstrap prerequisite; if missing,
open fails fast with an actionable error (privilege requirement named in
`docs/storage-backends.md`).

`pending_questions_vec`'s per-row dimension-mismatch skip (consensus.go:49)
becomes vacuous on a fixed-dim column — preserved as a store-level guard that
no-ops when dims match (they always do on Postgres) so the conformance suite can
exercise the skip path against SQLite.

**Dimension-change recovery** (documented, not automated): if
`storage.vector_dimension` disagrees with the existing column, open fails with an
error naming the remedy — drop and recreate the vector tables, invalidate
`output_index` (forces reconcile), re-embed via the existing `reembed` flow.
This mirrors what an embedding-model change requires on SQLite today.

### D7 — Migrations: Postgres gets its own embedded set; canonical time representation

`internal/storage/postgres/migrations/*.sql` via `embed`, same `schema_version`
pattern, append-only, **one statement per Exec** (pgx stdlib rejects
multi-statement prepared calls — known gotcha). SQLite's V1–V8 series untouched.
The Postgres set is written fresh at current schema shape and starts at V1.

**Time representation:** SQLite stores `datetime('now')` TEXT
(`'YYYY-MM-DD HH:MM:SS'`, UTC, tz-naive); Postgres uses
`TIMESTAMPTZ DEFAULT now()`. The store layer owns the conversion: every
time-typed field is scanned into `time.Time` on both backends — SQLite TEXT is
parsed **as UTC** at the scan boundary, Postgres scans arrive tz-aware and are
normalized with `.UTC()`. Conformance round-trips compare via
`.Equal`/`.UTC()`, never `==` (monotonic clock and location pointers would
flake). Nothing above the store sees a representation difference. This touches
scan helpers (`scanCompileItem`, trust scans) and is covered by conformance
tests round-tripping timestamps.

**Uniqueness parity:** Postgres schema deliberately does **not** add PK/unique
constraints beyond what SQLite enforces today (FTS5 `entries` has none; the
reconciler's Add-after-failed-Delete path at reconcile.go:242-249 relies on the
lenient behavior). Stricter constraints are a separate hardening task, not a
side effect of a backend port.

### D8 — Config; ~12 path sites audited

```yaml
storage:
  backend: sqlite            # sqlite | postgres (default sqlite)
  dsn: ${SAGE_WIKI_PG_DSN}   # env expansion; secrets never literal in file
  vector_dimension: 768      # required for postgres
  lock_timeout: 5s           # writer advisory-lock acquisition timeout (D9.2)
  pool: { max_open: 10, max_idle: 2 }
```

Threaded through `app.Open`. The 12 audited path sites (§2) keep the SQLite
convention when backend=sqlite (zero behavior change). Sites with no meaningful
Postgres analog get capability methods instead of rejecting postgres:
`doctor` → `Backend.Health()`; `status` schema probe → `Backend.SchemaReady()`;
compound-tool `DBPath` → `Backend.Location()`. `hub/search.go` opens each
project's DB by convention — it reads each project's config and dials its DSN
(postgres projects) or opens the file (sqlite projects); hub opens in **reader
mode** (no advisory lock, D9.2) with a **smaller read pool** (`max_open: 4`
per project, hard-coded for hub opens) — N projects × writer-sized pools would
exhaust Postgres `max_connections`; the budget note lives in
`docs/storage-backends.md`.

### D9 — Concurrency: identical write serialization on both backends (this task)

Spec §P2-1 says: *"Concurrency model: Postgres removes the single-writer
constraint — document how compile parallelism changes; keep SQLite path exactly
as-is."* The answer for P2-1: **it does not change.** The write-serialization
guarantees today come from three layers, and each gets a Postgres analog so the
effective semantics are identical:

1. **Process-local `writeMu` around `WriteTx`** — kept on the Postgres backend.
   Flows like `trust/promote.go` and `trust/hooks.go` do read-then-write
   sequences that were effectively serialized by `writeMu`; dropping it under
   Postgres READ COMMITTED would let them interleave — a behavior change
   disguised as infrastructure.
2. **Cross-process (D9.2): SQLite's DB file lock** (plus `manifest.WithLock` on
   reconcile paths) — reproduced on Postgres with a **writer-lock pair**, and
   readers are explicitly out of scope of the lock:

   - **Writer opens** (default; `app.Open` and all compile/write paths) acquire
     a session-level `pg_advisory_lock(sessionKey)` on a **dedicated
     `db.Conn(ctx)` pinned for the process lifetime** — never on the pool,
     where the lock would land on an arbitrary connection the pool can
     silently reap. Open uses try-lock with `storage.lock_timeout` (default
     `5s`): on timeout, open fails with an error naming that another
     sage-wiki writer process holds this vault's lock (the Postgres analog of
     SQLite's busy/locked error). This is the fail-fast deployment guard:
     P2-1 assumes a **single-writer-process** world and says so at startup,
     not at first write conflict.
   - **Every `WriteTx` and `BeginWrite` tx** additionally takes
     `pg_advisory_xact_lock(txKey)` as its first statement — the only variant
     scoped to the transaction itself, hence the only one that actually
     serializes writers regardless of which pooled connection executes. This
     defends correctness even if the pinned session connection dies silently.
   - **Two distinct keys — never one.** Advisory locks conflict across
     sessions regardless of scope: a shared key would make the writer's own
     first `WriteTx` block against its own pinned session lock and error
     after `lock_timeout` on *every* write. Derived as
     `sessionKey = int64(FNV-1a-64("sage-wiki:session:" + current_database()))`
     and `txKey = int64(FNV-1a-64("sage-wiki:tx:" + current_database()))`.
     Distinct vaults live in distinct databases on a shared cluster, so their
     keys never collide; two writers for the *same* vault contend exactly as
     intended. Vaults separated by schema (`search_path`) within one database
     share keys — harmless false serialization, documented.
   - **Reader opens** (hub multi-project search, any future read-only
     consumer) take **no advisory lock** — an exclusive session lock at open
     would serialize readers against each other and against the writer for no
     correctness benefit; MVCC gives readers consistent snapshots. Reader mode
     additionally: **skips migrations at open** (writer open runs them, as
     today on SQLite) and instead verifies `schema_version` read-only, failing
     with "vault schema is ahead of/behind this binary — run any writer
     command once" if the version mismatches; and **fails fast with a
     sentinel error on any `WriteTx`/`BeginWrite` call** — a reader that
     reaches a write path has a wiring bug, and silently taking the xact lock
      would turn it into a writer. **SQLite reader mode pins identical
      semantics** — skips migrations, verifies `schema_version`, and returns
      the same write-path sentinel — so the conformance suite asserts
      reader/writer behavior uniformly on both backends.
   - **Lock timeouts:** the open try-lock honors `storage.lock_timeout`
     (default `5s`); every write tx issues `SET LOCAL lock_timeout` to the
     same value before `pg_advisory_xact_lock`, matching SQLite's
     `busy_timeout=5000` behavior (a queued `WriteTx` errors after ~5s instead
     of hanging indefinitely behind a long reembed tx).
   - **Fail-fast error text** names both causes: a live second writer
     (stop it or point this process at another vault) and a crashed writer
     whose session Postgres has not yet reaped (remedy: inspect `pg_locks`,
     `pg_terminate_backend` the stale session — TCP keepalive can delay
     reaping).
   - **Close ordering:** `Backend.Close()` releases the pinned lock
     connection **before** closing the pool — deterministic, so a racing
     opener never sees a stale-lock error from a closing peer.
   - **Pool accounting:** the pinned lock `db.Conn` permanently consumes one
     of `pool.max_open` (database/sql counts held conns), so the effective
     pool is `max_open−1`, and `max_open−2` during a reembed tx. Stated in
     `docs/storage-backends.md` next to the pool config.
3. **Single write connection** — today `reembed.go:108`'s raw
   `WriteDB().Begin()` is *de facto serialized* against every `WriteTx` because
   there is one write connection; its long tx across network embedding calls
   blocks all other writers. Parity on Postgres therefore means reembed's tx
   **holds `writeMu` for its whole duration** — `Backend.BeginWrite() (*sql.Tx,
   error)` acquires the mutex (released on Commit/Rollback). On SQLite this
   newly *blocks* behind an in-flight `WriteTx` where the raw `Begin()`
   previously queued on the connection — same serialization semantics, stated
   once plainly. **Contract:** tx-scoped work inside a `BeginWrite` tx must not
   call `WriteTx` (the mutex is non-reentrant — self-deadlock), and
   Commit/Rollback is the only release path (callers use the deferred-rollback
   idiom). The conformance suite gains a BeginWrite-serialization test
   alongside the WriteTx one. Documented cost: the reembed tx pins one pool
   connection and an MVCC snapshot across network I/O — with default
   `pool.max_open: 10` this is noted in `docs/storage-backends.md` as the
   pool-sizing consideration.

Exploiting Postgres concurrency (relaxing the mutex, dropping the advisory lock
for multi-writer, isolation-level choices, `SELECT … FOR UPDATE` hardening) is
**explicitly deferred to P2-3** and recorded as such in
`docs/storage-backends.md`.

### D10 — Files-as-truth invariant preserved

The DB remains a rebuildable index on Postgres too: the P1-2 reconciler works
against both backends through the interfaces; `output_index` (drift comparand)
exists in both schemas. Postgres bootstrap for an existing vault = open empty DB
+ reconcile/recompile, never SQLite→Postgres data migration.

## 4. Test strategy

1. **Seam phase:** full existing suite green. Golden/user-visible-text checks
   (error prefixes byte-compared) are scoped to the **SQLite backend only** —
   driver error text is backend-specific by design.
2. **Conformance suite:** shared test package driven by a backend factory; runs
   against SQLite unconditionally and Postgres when `TEST_DATABASE_URL` is set.
   Covers every interface method incl. tx flows, the cache-invalidation
   contract, timestamp round-trips, CJK parity fixture, and the
   dimension-mismatch skip path. Excludes representation-specific behaviors:
   corrupt-blob guards (`vectors/store.go:196`) and BLOB encodings stay
   SQLite-backend unit tests. `SearchChunksFiltered`'s 100-docID cap is part of
   the interface contract — Postgres applies the same cap for parity.
   Concurrency: a `-race` leg plus a WriteTx-serialization test run against
   **both** backends (the property D9 pins).
3. **Boot path:** local container run before merge (env-gated CI means boot SQL
   — including `CREATE EXTENSION` failure messaging — is otherwise unexercised;
   recorded learning).

## 5. Sequencing (feeds plan.md)

1. Config `storage:` section + `app.Open` threading (no behavior change).
2. Interface extraction + escape-hatch moves incl. capability methods
   (zero behavior change; SQLite impls are today's code relocated).
3. Conformance suite against SQLite.
4. Postgres backend (pgvector registered per D1) + migrations; conformance
   matrix green.
5. Docs (`docs/storage-backends.md`: backend selection, `CREATE EXTENSION`
   prerequisite, dimension-change recovery, concurrency deferral to P2-3),
   config scaffolding, CHANGELOG.

## 6. Explicit non-goals

Compile parallelism changes (P2-3 — documented deferral), pgx-native protocol,
property-graph backends, SQLite→Postgres data migration, exact FTS ranking
parity, CJK recall improvements, relaxing the write mutex, stricter uniqueness
constraints, retiring the cache-invalidation wart, runtime HNSW auto-tuning.
