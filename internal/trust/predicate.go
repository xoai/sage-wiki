package trust

import "strings"

// ConfirmationChecker is the one thing the rule needs from a trust store.
// Taking the interface (not *Store) lets any backend's trust store answer —
// the hub reads projects through store.Backend, and a nil there would have
// silently excluded even confirmed outputs.
type ConfirmationChecker interface {
	IsConfirmed(docID string) bool
}

// IncludePredicate returns the doc-inclusion rule for a trust.include_outputs
// mode. Non-output docs are always included; `output:` docs (LLM-generated
// answers auto-filed back into the wiki) are included only as the mode allows:
//
//	"true"     — always
//	"verified" — only when the trust store has confirmed the output
//	anything else (including the "false" default) — never
//
// This is the single definition of the rule. The Q&A context builder applies
// it inline; the search entry points inject it as search.Deps.IncludeDoc, so
// an agent searching the wiki sees exactly what a Q&A answer would cite.
// A nil store with mode "verified" excludes every output — nothing can be
// verified without one.
func IncludePredicate(mode string, ts ConfirmationChecker) func(docID string) bool {
	return func(docID string) bool {
		if !strings.HasPrefix(docID, "output:") {
			return true
		}
		switch mode {
		case "true":
			return true
		case "verified":
			if ts == nil || isNilChecker(ts) {
				return false
			}
			return ts.IsConfirmed(strings.TrimPrefix(docID, "output:"))
		default:
			return false
		}
	}
}

// isNilChecker catches a typed-nil interface value (a nil *Store assigned to
// the interface is non-nil as an interface), which would panic on call.
func isNilChecker(c ConfirmationChecker) bool {
	if s, ok := c.(*Store); ok {
		return s == nil
	}
	return false
}
