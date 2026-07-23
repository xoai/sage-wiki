package trust

import "github.com/xoai/sage-wiki/internal/store"

type OutputState = store.OutputState

const (
	StatePending   = store.StatePending
	StateConfirmed = store.StateConfirmed
	StateConflict  = store.StateConflict
	StateStale     = store.StateStale
)

type PendingOutput = store.PendingOutput

type Confirmation = store.Confirmation
