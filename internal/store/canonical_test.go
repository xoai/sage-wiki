package store

import (
	"errors"
	"testing"
)

// erroringChain implements only what CanonicalOrSelf touches.
type erroringChain struct {
	OntologyStore
	id  string
	err error
}

func (e erroringChain) CanonicalID(id string) (string, error) {
	if e.err != nil {
		return "", e.err
	}
	return e.id, nil
}

func TestCanonicalOrSelfKeepsRawOnError(t *testing.T) {
	got := CanonicalOrSelf(erroringChain{err: errors.New("boom")}, "alias")
	if got != "alias" {
		t.Errorf("CanonicalOrSelf on error = %q, want the raw input back — "+
			"a failed chain read must degrade to the unresolved view, not fail the caller", got)
	}
	if got := CanonicalOrSelf(erroringChain{id: "canon"}, "alias"); got != "canon" {
		t.Errorf("CanonicalOrSelf = %q, want canon", got)
	}
}
