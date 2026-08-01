package engine

import (
	"errors"
	"regexp"
)

// ErrInvalidWorkspaceName reports a workspace name that fails the charset
// rule — including every traversal payload (`..`, separators, URL-encoded
// variants), which the allowed charset rejects by construction.
var ErrInvalidWorkspaceName = errors.New("engine: invalid workspace name")

// workspaceNameRe is the single source of truth for workspace names in a
// Manager registry: one path segment, lowercase, 1-64 chars, alphanumeric
// start. Any byte outside the class (/.%\ etc.) fails the match — there is
// no encoded form of a separator inside the allowed alphabet.
var workspaceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-_]{0,63}$`)

// ValidateWorkspaceName checks name against the registry charset rule.
// It returns ErrInvalidWorkspaceName (wrapped) on rejection.
func ValidateWorkspaceName(name string) error {
	if !workspaceNameRe.MatchString(name) {
		return ErrInvalidWorkspaceName
	}
	return nil
}
