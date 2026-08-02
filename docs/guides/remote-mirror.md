# Remote Mirror — S3-Compatible Backup and Hydrate

The remote mirror gives every workspace durable, continuous replication to
an S3-compatible bucket (S3, R2, MinIO) and fast restore from that bucket.
The local directory remains the operating surface — the mirror is
durability and mobility, never a live query path.

## Quick start

```bash
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...
# config.yaml:
# mirror:
#   endpoint: "https://<account>.r2.cloudflarestorage.com"
#   bucket: "my-wiki-backups"
sage-wiki mirror enable
sage-wiki mirror status
```

From then on: `serve` ships continuously (`ship_interval`, default 1s),
and every CLI command runs a best-effort ship pass after it finishes
(success or error — bucket unreachable only prints a warning).

## What ships (and what does not)

Ships: `wiki/`, `raw/`, `prompts/`, `.manifest.json`, `.sage/wiki.db`
(Litestream-style: generation snapshot + WAL segments), `.sage/vectors*.idx`,
`.sage/manifest.json`, `.sage/pack-state.yaml`.

Never ships: `config.yaml` (can hold inline secrets — hydrate restores data
only), `engine.lock`, `jobs.jsonl`, `batch-state.json`,
`compile-state.json`, `usage.jsonl`, `lintlog/`, `pack-snapshots/`,
`*.tmp`, and the mirror's own machine-local files.

## Crash safety

The commit pointer (`mirror-state.json`) is always written LAST, after
every object it references is durably uploaded. A kill at any byte leaves
the previous committed generation restorable:

```bash
sage-wiki mirror verify          # full re-download re-hash of every object
sage-wiki mirror verify --fast   # existence-only (HEAD) pass
```

Verify also checks every retained rotated generation (point-in-time
restore targets) and reports unreferenced orphan objects as advisories.

## Restore

```bash
sage-wiki hydrate s3://bucket/prefix /path/to/empty-dir
sage-wiki hydrate s3://bucket/prefix dir --generation 3
sage-wiki hydrate s3://bucket/prefix dir --at 2026-08-01T12:00:00Z
sage-wiki hydrate s3://bucket/prefix dir --partial
sage-wiki hydrate s3://bucket/prefix dir --key-file ~/.config/sage/mirror.key
```

- Restore requires an EMPTY directory (no merge semantics), except a
  `--partial` resume.
- `--at` is segment-granular: it lands on the last WAL segment sealed at
  or before TIME; any overshoot (≤ 1 segment) is printed. Timestamps come
  from the mirror's own records, never bucket metadata.
- **PITR scope:** `--at`/`--generation` select the database chain AND the
  markdown set from the same generation. Every rotation seals the
  generation's doc/vector object map into its meta.json, so a
  rotated-generation restore is a consistent tree. Granularity is
  per-generation for docs (per-segment for the db): a mid-generation
  delete may still be present and a mid-generation create may be
  missing — an `--at` restore report prints both skews (excluded
  segments and "objects at generation N's seal"; `--generation` is
  seal-consistent by construction). Mirrors written before object maps
  fall back to docs at newest with a printed note
  (`note: generation has no object map; docs restored at newest`).
- `--partial` writes progress markers and prints when lexical/graph is
  available (before vectors finish); a follow-up `--partial` resumes.
- Addressing: `mirror.addressing: auto` uses virtual-host style for AWS
  endpoints (path-style is deprecated there) and path-style elsewhere
  (MinIO/R2); `path`/`virtual` force a style.
- Credentials: static keys via env (names configurable) or a
  credentials_file; **STS temporary credentials are supported** via
  `session_token_env` (default AWS_SESSION_TOKEN) or a `session_token`
  key in the credentials file. The token must come from the SAME source
  as the keys — env keys + a file token is a hard error at enable
  (signing without a token yields opaque 403s). An empty token value
  reads as absent.
- Credentials for hydrate read from `AWS_ACCESS_KEY_ID` /
  `AWS_SECRET_ACCESS_KEY` or `--credentials-file` (no workspace config
  exists yet); `--region` defaults to `auto`, and `--endpoint` is required
  for R2/MinIO (derived from `--region` for AWS). STS: hydrate also picks
  up `AWS_SESSION_TOKEN` by default — and `--credentials-file` plus an
  ambient token env var is a hard error (same-source rule). Equally: env
  keys with an UNREADABLE credentials file configured is now a hard
  error (previously env won silently).
- Error timing: a stalled small call can now take up to
  retries × 30s + backoff to report (per-attempt floors) — the trade for
  large snapshots completing at up to 15m.

## Encryption (optional)

```yaml
mirror:
  encryption:
    enabled: true
    key_file: "/home/you/.config/sage/mirror.key"  # 32 bytes, OUTSIDE the workspace
```

AES-256-GCM per shipped object. `mirror verify` works without the key
(integrity hashes cover the shipped ciphertext). Hydrate without the
correct `--key-file` fails loudly.

## Operations

- `mirror status` fields: enabled, remote generation, last commit,
  pending changes, pending rotation, rotation_deferred (busy-writer
  starvation signal), lag seconds, and a serve-restart note when a serve
  holds the workspace.
- `retain_generations` bounds point-in-time depth in ROTATION COUNT, not
  time — under CLI-heavy churn, depth may be minutes; raise it for
  PITR-heavy deployments. The value is recorded in mirror-state.json at
  every commit, and **`mirror verify` uses the state's recorded value**
  (not your local config) when checking rotated generations — so verify
  works correctly from any machine, and old states without the field
  fall back to local config.
- Timeouts: S3 calls use a per-attempt, payload-scaled timeout
  (30s floor, 3×(size/256KiB·s), 15m cap) — a large snapshot on a slow
  uplink finishes; a stalled server is cut at the attempt cap.
- The standalone `sage-wiki tui` has no in-process shipper: its changes
  ship at TUI exit (on kill -9, at the next command). `serve` and
  `serve --ui` ship continuously.
- RPO: induced loss ≤ `ship_interval` of writes with the shipper running
  (measured in `internal/mirror/rpo_test.go`).
- Signing is verified against the vendored aws4_testsuite
  (`internal/mirror/s3/testdata/aws4_testsuite`, botocore, Apache-2.0)
  with derived S3-shaped expectations, covering STS session-token
  signing. **Live smoke (maintainer-run):** `SAGE_TEST_AWS=1
  SAGE_TEST_AWS_BUCKET=<bucket> AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...
  go test ./internal/mirror/ -run TestLiveAWS_RoundTrip` — never in CI.
