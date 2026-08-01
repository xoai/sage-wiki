package mirror

import "context"

// ChangeSource detects unshipped workspace changes (spec.md §Components:
// the SPEC-04 seam — the deterministic change detector SPEC-04 introduces
// plugs in here; the mtime/manifest-diff default lands in Task 14).
type ChangeSource interface {
	// Changes returns workspace changes since the token, and the next token.
	Changes(ctx context.Context, since ChangeToken) ([]Change, ChangeToken, error)
}

// ChangeToken is an opaque position in the change stream.
type ChangeToken struct {
	// StateUpdatedAt is the committed mirror-state's updated_at the diff
	// ran against (zero for "never shipped").
	StateUpdatedAtUnix int64
}

// ChangeKind classifies a detected change.
type ChangeKind int

const (
	// ChangeUpsert is a new or modified file.
	ChangeUpsert ChangeKind = iota
	// ChangeDelete is a removed file (tombstone).
	ChangeDelete
)

// Change is one detected workspace change.
type Change struct {
	Path    string // workspace-relative slash path
	Kind    ChangeKind
	SHA256  string // content hash (upserts only)
	ModUnix int64
}
