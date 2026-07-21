package store

import (
	"crypto/sha256"
	"fmt"
)

// LearningID generates a deterministic dedup ID for a learning entry.
// The algorithm moved here from linter (P2-1) so both storage backends share
// exactly one implementation; linter.LearningID delegates to this.
func LearningID(content string) string {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	return "learn-" + hash[:16]
}
