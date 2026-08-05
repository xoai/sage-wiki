// Package pathsafe provides symlink-aware, sibling-prefix-safe path containment
// for filesystem-serving and filesystem-writing handlers. It is the single place
// the project answers "does this untrusted path stay inside the directory I mean
// to expose?" — replacing ad-hoc strings.HasPrefix checks that miss sibling
// prefixes (/a/wiki vs /a/wiki-secret) and symlink escapes. Since SPEC-08 it
// also answers the input-shape half of ingestion safety (ValidateRel,
// ValidConceptID).
//
// It depends only on the standard library (plus golang.org/x/text for NFKC
// lookalike detection) so it can be imported anywhere without an import cycle.
package pathsafe

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/xoai/sage-wiki/internal/limits"
)

// SafeJoin joins untrustedRel under base and returns the cleaned absolute path,
// or an error if the result would resolve outside base (including via a symlink).
// For a not-yet-existing target (e.g. a write destination), it verifies the
// nearest existing ancestor is contained. Callers translate the error to a 403.
func SafeJoin(base, untrustedRel string) (string, error) {
	// filepath.Join cleans the result, collapsing any ".." in untrustedRel; an
	// escaping relative path therefore lands outside base and is caught below.
	joined := filepath.Join(base, untrustedRel)
	ok, err := Contained(base, joined)
	if err != nil {
		return "", fmt.Errorf("pathsafe.SafeJoin: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("pathsafe.SafeJoin: %q escapes %q", untrustedRel, base)
	}
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("pathsafe.SafeJoin: %w", err)
	}
	return abs, nil
}

// Contained reports whether target resolves to a path inside base. It is
// symlink-safe (resolves the deepest existing ancestor of both) and
// sibling-prefix-safe. It fails closed: any Abs/EvalSymlinks error returns
// (false, err) rather than assuming containment.
func Contained(base, target string) (bool, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return false, fmt.Errorf("pathsafe.Contained: abs base: %w", err)
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false, fmt.Errorf("pathsafe.Contained: abs target: %w", err)
	}

	// Resolve symlinks on the deepest existing ancestor of base as well, so a
	// base whose leaf does not exist yet (e.g. the output dir before the first
	// compile) is handled gracefully — a legit path under it stays contained and
	// the caller can 404 — instead of being reported as a traversal attempt.
	// base is a trusted path, so a lexical not-yet-created tail is fine; the
	// security-critical symlink resolution is on the untrusted target below.
	resolvedBase, err := resolveExisting(absBase)
	if err != nil {
		return false, fmt.Errorf("pathsafe.Contained: resolve base: %w", err)
	}
	// target may not exist yet (write destination); resolve its existing prefix.
	resolvedTarget, err := resolveExisting(absTarget)
	if err != nil {
		return false, fmt.Errorf("pathsafe.Contained: resolve target: %w", err)
	}

	if resolvedTarget == resolvedBase {
		return true, nil
	}
	// Separator-terminated prefix so /a/wiki does not match /a/wiki-secret.
	return strings.HasPrefix(resolvedTarget, resolvedBase+string(filepath.Separator)), nil
}

// resolveExisting resolves symlinks on the deepest existing ancestor of path and
// re-appends the non-existing tail. This lets Contained check a write target
// that does not exist yet: the parent chain that DOES exist is resolved (so an
// intermediate symlink cannot smuggle the path out of base), while the final
// not-yet-created components are treated literally.
func resolveExisting(path string) (string, error) {
	p := filepath.Clean(path)
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			full := resolved
			// Re-append the tail we peeled off, outermost last.
			for i := len(tail) - 1; i >= 0; i-- {
				full = filepath.Join(full, tail[i])
			}
			return full, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			// A real error (permission, ELOOP, …) — fail closed.
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			// Reached the filesystem root without finding an existing ancestor.
			return "", err
		}
		tail = append(tail, filepath.Base(p))
		p = parent
	}
}

// MaxNameLen is the per-segment name ceiling (SPEC-08 AC1 "overlong names").
const MaxNameLen = 255

// encodedTraversalRe matches URL-encoded dot/slash/backslash sequences —
// ingestion APIs receive raw strings, so a literal %2e%2e%2f is a hostile
// encoding attempt, not a decoded separator (SPEC-08 AC1).
var encodedTraversalRe = regexp.MustCompile(`(?i)%(2e|2f|5c)`)

// conceptIDRe is the shared concept-identifier shape (SPEC-08 D3): the
// REST edge rule (internal/api conceptIDShape) promoted to the single
// ingestion-guard home.
var conceptIDRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidConceptID reports whether s matches the concept-identifier charset.
func ValidConceptID(s string) bool {
	return conceptIDRe.MatchString(s)
}

// ValidateRel rejects untrusted workspace-relative path inputs that could
// traverse, confuse, or overflow path construction (SPEC-08 AC1). Traversal
// vectors unwrap to limits.ErrTraversalTooWide; malformed inputs (NUL,
// overlong names, empty) unwrap to limits.ErrInvalidName. It is the shared
// predicate for every API that accepts a workspace-relative path; callers
// still run Contained/SafeJoin as the containment backstop.
func ValidateRel(rel string) error {
	if rel == "" {
		return fmt.Errorf("pathsafe: empty path: %w", limits.ErrInvalidName)
	}
	if strings.IndexByte(rel, 0) >= 0 {
		return fmt.Errorf("pathsafe: NUL byte in path: %w", limits.ErrInvalidName)
	}
	if strings.ContainsRune(rel, '\\') {
		return fmt.Errorf("pathsafe: backslash in path %q: %w", rel, limits.ErrTraversalTooWide)
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("pathsafe: absolute path %q: %w", rel, limits.ErrTraversalTooWide)
	}
	if encodedTraversalRe.MatchString(rel) {
		return fmt.Errorf("pathsafe: encoded separator in path %q: %w", rel, limits.ErrTraversalTooWide)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return fmt.Errorf("pathsafe: parent traversal in path %q: %w", rel, limits.ErrTraversalTooWide)
		}
		if len(seg) > MaxNameLen {
			return fmt.Errorf("pathsafe: name longer than %d bytes in path: %w", MaxNameLen, limits.ErrInvalidName)
		}
	}
	if lookalikeRune(rel) {
		return fmt.Errorf("pathsafe: unicode lookalike separator in path %q: %w", rel, limits.ErrTraversalTooWide)
	}
	return nil
}

// lookalikeRune reports whether rel contains a non-ASCII rune whose NFKC
// normalization yields a path-significant ASCII character ('.', '/', '\\',
// '%') — e.g. two-dot leader U+2025 or fullwidth solidus U+FF0F.
func lookalikeRune(rel string) bool {
	for _, r := range rel {
		if r < 0x80 {
			continue
		}
		n := norm.NFKC.String(string(r))
		if strings.ContainsAny(n, "./\\%") {
			return true
		}
	}
	return false
}
