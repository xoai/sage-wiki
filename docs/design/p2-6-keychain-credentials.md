# Design: P2-6 — Keychain-backed credentials

**Status:** draft (first commit of PR per Phase-2 spec preamble)
**Spec:** `.sage/docs/sage-wiki-upgrade/06-spec-phase2-strategic.md` §P2-6
**Cycle:** `.sage/work/20260724-p2-6-keychain-credentials/`

## 1. Problem

Credentials live in `~/.sage-wiki/auth.json` (0600). On OSes with a real
keychain that's plaintext-on-disk where an OS store is free. The store
API surface is small (`Get/Put/Delete/List/TOS`) and every call site
already takes `*Store`, so a backend-selecting Store needs no call-site
changes.

## 2. Design decisions

### D1 — Backend selection by read-only probe at construction

`NewStore(path)` keeps its signature. At construction it probes the
keyring with a READ of a dedicated probe key
(`keyring.Get("sage-wiki:probe")`): returns successfully OR
`keyring.ErrNotFound` → keychain available; any other error (no dbus,
locked, denied, headless) → file backend. The probe NEVER writes (a
write probe could create keychain entries on locked stores, prompt on
dbus, or alter user state at construction time).

The selected backend is stored on the Store and exposed via
`Store.Backend() string` ("keychain" | "file") for `auth status`.

### D2 — One keyring entry per provider, full-fidelity JSON

Each provider's credential is one keyring entry
(`sage-wiki:<provider>`) holding the credential JSON. Round-trip must
preserve: AccessToken, RefreshToken (including `""`), ExpiresAt
(including `0` = no-expiry direct tokens — memory: refresh is gated on
`RefreshToken != ""` and `ExpiresAt==0` means valid-not-expired),
Provider, and any provider extras (ExtraHeaders inputs). The JSON shape
reuses the existing Credential marshal (no new schema). TOS flag stays
FILE-based always (not a credential; the file backend owns TOS
regardless of selected backend — `IsTOSAcknowledged`/`AcknowledgeTOS`
always hit the file).

### D3 — Idempotent, non-destructive first-run migration

On Store construction with keychain backend active: if the file has
credentials AND no keychain entries exist for this app (List returns
empty for our prefix): copy every file credential into the keyring,
print one notice line naming the file PATH (never token values), and
keep the file untouched as backup. Guard: migration runs only when the
keychain List is empty, so it can never overwrite newer keychain state,
and it never deletes the file. Partial failure: a credential that fails
to write is left in the file AND read from the file on Get (D4's
fallback read) — migration is best-effort, never blocks startup.

### D4 — Read fallback chain: keychain → file

`Get(provider)`: keychain backend → try keyring; on
`keyring.ErrNotFound` → try the file (covers pre-migration state,
partial migration, and manual keychain deletion). File backend → file
only. This makes the migration genuinely non-destructive: deleting the
file would still be recoverable only if keychain had the cred, but we
never delete it anyway.

### D5 — `auth status` reports backend + location

Status output gains a backend line (active backend: keychain|file) and
per-credential location (found in keychain vs file). No secret material
in output (existing redaction rules unchanged).

### D6 — Dependency: zalando/go-keyring, pure Go

Spec-named dependency. Verified pure Go (no cgo on any platform:
darwin Security Framework via pure-Go syscall shims, Windows via
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
