# Design: P2-6 — Keychain-backed credentials

**Status:** draft, review iteration 2 (first commit of PR per Phase-2 spec preamble)

> Iteration log: i1 found 1C/3M/4S/1cos — logout resurrection (Delete only
> from keychain + file backup + read fallback = plaintext resurrection),
> go-keyring has NO List/enumerate API (migration guard unimplementable as
> written), Linux locked-keyring probe can prompt and block, spec's "offer
> to move" vs auto-migrate deviation, missing fields (Source/AccountID/
> Provider rehydration), Put write policy unspecified, probe caching,
> per-credential location reporting. All folded in below.
**Spec:** `.sage/docs/sage-wiki-upgrade/06-spec-phase2-strategic.md` §P2-6
**Cycle:** `.sage/work/20260724-p2-6-keychain-credentials/`

## 1. Problem

Credentials live in `~/.sage-wiki/auth.json` (0600). On OSes with a real
keychain that's plaintext-on-disk where an OS store is free. The store
API surface is small (`Get/Put/Delete/List/TOS`) and every call site
already takes `*Store`, so a backend-selecting Store needs no call-site
changes.

## 2. Design decisions

### D1 — Backend selection by read-only probe with a hard timeout (i1)

`NewStore(path)` keeps its signature. At construction it probes the
keyring with a READ of a dedicated probe key
(`keyring.Get("sage-wiki:probe")`): success OR `keyring.ErrNotFound` →
keychain available; any other error → file backend. **The probe runs in
a goroutine with a 500ms hard timeout** (i1: a locked Linux Secret
Service collection can block with a dbus unlock PROMPT rather than
returning an error — the timeout maps prompt/lock/headless to the file
backend; the goroutine is left to complete in the background and is
harmless — it holds no state and writes nothing). The probe NEVER
writes.

**Per-process probe caching (i1):** the probe result is computed ONCE
per process via sync.Once — resolve.go builds the store twice per client
construction and every auth subcommand builds its own; without caching
the dbus cost/prompt risk multiplies per invocation.

### D2 — One keyring entry per provider, full-fidelity JSON (i1 fields)

Each provider's credential is one keyring entry
(`sage-wiki:<provider>`) holding the credential JSON. Round-trip must
preserve ALL fields: AccessToken, RefreshToken (including `""`),
ExpiresAt (including `0` = no-expiry direct tokens), **Source** (shown
in `auth status`), **AccountID** (drives openai ExtraHeaders), and
**Provider** — which is `json:"-"` and NEVER marshaled: the keychain
backend rehydrates Provider from the ENTRY KEY name
(`sage-wiki:<provider>`), exactly as store.read rehydrates from the map
key. The round-trip test asserts every one of these fields explicitly.
TOS flag stays FILE-based always (not a credential; the file backend
owns TOS regardless of selected backend).

### D3 — Migration is an explicit `auth migrate` command (i1 spec-compliance)

The Phase-2 spec says "OFFER to move existing file creds into the
keychain" — auto-copying tokens into a new store without consent is a
UX/security deviation. Pinned: NO automatic migration. Instead:
- A new `sage-wiki auth migrate` subcommand performs the copy (file →
  keychain for every credential the closed provider set resolves in the
  file), prints per-provider "moved" lines (never token values), and
  leaves the file intact as backup.
- On the FIRST run with the keychain backend active and file creds
  present, one stderr notice line: "credentials available in OS keychain
  — run `sage-wiki auth migrate` to move them (file fallback unchanged)".
  Non-interactive (never blocks a pipeline), printed at most once per
  state (suppressed once no file creds remain or after migration).
**Enumeration without a List API (i1):** go-keyring exposes only
Get/Set/Delete/DeleteAll — no enumeration. `auth migrate` and
`Store.List()` enumerate the CLOSED provider set (the providers auth
knows about) with per-provider Get probes; no keychain prefix-scanning
exists or is needed.

### D4 — Read fallback, but Delete and Put are dual-backend (i1 CRITICAL)

`Get(provider)`: keychain backend → try keyring; on
`keyring.ErrNotFound` → try the file (covers pre-migration state and
partial migration). File backend → file only.

**Delete operates on BOTH backends** (the i1 CRITICAL: keychain-only
Delete + retained file + read fallback = plaintext resurrection —
`auth logout` reports success while the credential still resolves).
Delete removes the keyring entry (ignore ErrNotFound) AND the file entry
when the file backend owns that credential (post-migration the file
still holds it).

**Put policy (i1):** Put writes to the ACTIVE backend only — keychain
when active. The file backup is POINT-IN-TIME (frozen at migration):
new/rotated secrets never touch plaintext disk again (that IS the
SEC-12 win), and the stale file copy is never resurrected as newer
because Delete clears it and the notice/status make the backup's
frozen-ness explicit. Refresh flows rotate via Put → keychain stays
current.

### D5 — `auth status` reports backend + location (i1 mechanism)

Status output gains a backend line (active backend: keychain|file).
Per-credential location is reported by PROBING each known provider in
BOTH backends directly (no List signature change): found-in-keychain,
found-in-file, or both (post-migration both is expected until the user
deletes one side). `Store.List()` itself returns the merged union
(keychain ∪ file, keychain winning on conflicts — it's the active
backend). No secret material in any output.

### D6 — Dependency: zalando/go-keyring, pure Go

Spec-named dependency. Verified pure Go (no cgo on any platform:
darwin backend shells out to /usr/bin/security via os/exec (CGO-free), Windows via
wincred, Linux via Secret Service dbus). One new module; pinned
version. `CGO_ENABLED=0 go build ./...` must stay green (release
invariant).

### D7 — Secrets discipline

No credential values in any log line, notice, status output, or error
message. The migration notice names only the file path. Status shows
backend/location/expiry, never tokens. Keychain entry keys use the
`sage-wiki:` prefix; no other app's keys are read.

## 3. Non-goals

Config flag for backend selection, TOS migration, keychain clear/delete
command, transport/refresh changes, CI behavior changes, new credential
fields.

## 4. Test strategy

- Backend probe: keyring-available vs error → backend selection (mock
  the keyring layer — the keyring calls live behind a tiny interface so
  tests never touch a real keychain).
- Round-trip: credential with all field shapes (refresh=="" , expiry==0,
  extras) survives keychain JSON encode/decode byte-identically.
- Migration: file-creds + empty keychain → copied once, file kept,
  second run no-op; partial write failure → remaining creds still
  readable from file.
- Read fallback: keychain miss → file hit.
- auth status: backend line + per-credential location, no secrets.
- TOS: file-owned on both backends.
- Full suite + `CGO_ENABLED=0 go build ./...` green.
