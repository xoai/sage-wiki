package metrics

import "github.com/xoai/sage-wiki/internal/log"

// LogSnapshot emits the current registry as one structured log line, or
// nothing when the registry has no recordings (empty-guard, spec §3).
func LogSnapshot() {
	if snap := Snapshot(); snap != nil {
		log.Info("metrics snapshot", snap...)
	}
}
