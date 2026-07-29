package ontology

import "time"

// P3-6: bi-temporal edge validity.
//
// An edge is LIVE AT T iff (valid_from unset OR valid_from <= T) AND
// (valid_to unset OR valid_to > T). All writer-produced values are RFC3339
// UTC, so TEXT comparison is chronological; COALESCE maps both legacy NULL
// (Postgres binds "" to NULL) and SQLite '' to "unset". valid_to is strict:
// the edge stopped being true AT valid_to.

// liveAtPredicate returns the SQL fragment for "edge live at ?", with columns
// qualified by alias ("" for relations, "d." for the derived arm). One
// definition for every read path so the two union arms can never diverge.
func liveAtPredicate(alias string) string {
	vf := alias + "valid_from"
	vt := alias + "valid_to"
	return "(COALESCE(" + vf + ",'')='' OR COALESCE(" + vf + ",'')<=?)" +
		" AND (COALESCE(" + vt + ",'')='' OR COALESCE(" + vt + ",'')>?)"
}

// asOfString normalizes the probe time to the same format writers use.
func asOfString(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
