package events

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewID returns a fresh sortable unique identifier (event IDs and
// compile-job IDs share the scheme).
func NewID() string {
	return newID(time.Now().UTC())
}

// newID builds a sortable unique event ID: a fixed-width hex prefix of the
// unix millisecond (lexicographic order == generation order) plus 16 random
// hex chars from crypto/rand. The timestamp prefix lets consumers sort an
// unordered batch without parsing the envelope time.
func newID(now time.Time) string {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		// crypto/rand failing means the CSPRNG is broken process-wide;
		// an event ID is not worth hiding that. Fail loudly.
		panic(fmt.Sprintf("events: crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%012x-%s", now.UnixMilli(), hex.EncodeToString(rnd[:]))
}
