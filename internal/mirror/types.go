package mirror

import "time"

// durationField keeps time.Duration fields greppable as config-resolved
// values (cmd layer converts config strings to these).
type durationField = time.Duration

// NotReadyError reports a facade call before the M2-M4 internals are wired.
type NotReadyError struct{ Op string }

func (e *NotReadyError) Error() string {
	return "mirror: " + e.Op + " internals not wired (mirror not enabled)"
}

// Status is `mirror status` output (spec.md §CLI fields).
type Status struct {
	Enabled          bool      `json:"enabled"`
	RemoteGeneration int       `json:"remote_generation"`
	LastCommit       time.Time `json:"last_commit"`
	PendingChanges   int       `json:"pending_changes"`
	PendingRotation  bool      `json:"pending_rotation"`
	RotationDeferred int       `json:"rotation_deferred"`
	LagSeconds       int64     `json:"lag_seconds"`
	ServeRestartNote bool      `json:"serve_restart_note,omitempty"`
}

// Report is `mirror verify` / hydrate output.
type Report struct {
	Valid      bool     `json:"valid"`
	Checked    int      `json:"checked"`
	Violations []string `json:"violations,omitempty"`
	Advisories []string `json:"advisories,omitempty"`
	Generation int      `json:"generation,omitempty"`
	RestoredTo string   `json:"restored_to,omitempty"`
	Overshoot  string   `json:"overshoot,omitempty"`
}

// HydrateOpts tunes a hydrate (spec.md §APIs).
type HydrateOpts struct {
	Generation int
	At         time.Time
	Partial    bool
	KeyFile    string
}

// RemoteRef is a parsed s3:// URL + flags for the pre-workspace hydrate path.
type RemoteRef struct {
	Endpoint, Bucket, Prefix, Region string
}
