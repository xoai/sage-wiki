package store

// CanonicalOrSelf resolves id through the applied-alias chain, returning the
// input id unchanged when resolution fails.
//
// HANDLED-BY-FALLBACK, deliberately not logged: every consumer is a best-effort
// read boundary (query seeds, MCP/web/CLI traverse args) where the trade is a
// partially-resolved view versus none, and a mixed resolved/unresolved set is
// accepted rather than accidental. It does not log because this package has
// zero internal imports — a property worth more than a debug line on a
// best-effort path. Store methods themselves never resolve (D2): this helper
// exists so CONSUMER boundaries can, uniformly.
func CanonicalOrSelf(os OntologyStore, id string) string {
	cid, err := os.CanonicalID(id)
	if err != nil {
		return id
	}
	return cid
}
