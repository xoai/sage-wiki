// Package mirror defines the remote-mirror contract (SPEC-01 seam;
// implementations arrive with SPEC-03). A Mirror replicates a workspace to
// remote storage and hydrates an empty local dir back from it.
package mirror

import "context"

// SnapshotID identifies a shipped snapshot.
type SnapshotID string

// ChangeBatch is one unit of replication (WAL segment and/or objects).
type ChangeBatch struct {
	ID      string
	Payload []byte
}

// Mirror is the remote replication seam. Remote storage is a MIRROR
// (backup/replicate/hydrate), never a live query path.
type Mirror interface {
	// Hydrate restores a remote snapshot into an empty local dir.
	Hydrate(ctx context.Context, dst string) error
	// Ship sends one change batch to the remote.
	Ship(ctx context.Context, batch ChangeBatch) error
	// Snapshot marks the current replicated state and returns its id.
	Snapshot(ctx context.Context) (SnapshotID, error)
}
