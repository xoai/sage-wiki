// Package traversaltest holds the shared SPEC-08 AC1 traversal case table.
// It exists as its own package because the table is consumed by tests in
// multiple packages (internal/compiler, internal/mcp, cmd/sage-wiki,
// internal/pathsafe) — a package-main test helper cannot be imported.
package traversaltest

import "strings"

// Case is one hostile input every path/name-accepting API must reject with
// a typed error (SPEC-08 AC1).
type Case struct {
	Name  string
	Input string
	// Family classifies the expected typed error: "traversal" cases must
	// unwrap to limits.ErrTraversalTooWide, "malformed" to
	// limits.ErrInvalidName.
	Family string
}

// OverlongName is a 300-char path segment (> the 255 name limit).
var OverlongName = strings.Repeat("a", 300) + ".md"

// Cases returns the AC1 table.
func Cases() []Case {
	return []Case{
		{Name: "absolute", Input: "/etc/passwd", Family: "traversal"},
		{Name: "dotdot", Input: "../secret.md", Family: "traversal"},
		{Name: "dotdot-deep", Input: "../../etc/passwd", Family: "traversal"},
		{Name: "dotdot-mid", Input: "raw/../../secret.md", Family: "traversal"},
		{Name: "backslash", Input: `..\secret.md`, Family: "traversal"},
		{Name: "encoded-separators", Input: "%2e%2e%2fsecret.md", Family: "traversal"},
		{Name: "encoded-separators-upper", Input: "%2E%2E%2Fsecret.md", Family: "traversal"},
		{Name: "unicode-lookalike-dots", Input: "\u2025/secret.md", Family: "traversal"},
		{Name: "unicode-lookalike-slash", Input: "raw\uff0fsecret.md", Family: "traversal"},
		{Name: "nul", Input: "raw/a\x00b.md", Family: "malformed"},
		{Name: "overlong-name", Input: OverlongName, Family: "malformed"},
	}
}
