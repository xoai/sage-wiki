package serve

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/xoai/sage-wiki/internal/compiler"
)

// LoadTokens resolves the token set: --token flag wins when present
// (explicit beats file); otherwise the token file's lines; env/config
// stay fallbacks for the legacy flags. Returns the resolved union.
func LoadTokens(flagToken, tokenFile, envToken, configToken string) ([]string, error) {
	if flagToken != "" {
		return []string{flagToken}, nil
	}
	if tokenFile != "" {
		raw, err := os.ReadFile(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read token file: %w", err)
		}
		if info, err := os.Stat(tokenFile); err == nil && info.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(os.Stderr, "warning: token file %s is group/world-readable (want 0600)\n", tokenFile)
		}
		var tokens []string
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			tokens = append(tokens, line)
		}
		return tokens, nil
	}
	if envToken != "" {
		return []string{envToken}, nil
	}
	if configToken != "" {
		return []string{configToken}, nil
	}
	return nil, nil
}

// CheckRefusal enforces the bind-safety invariant: a non-loopback
// address with zero resolved tokens is a hard error naming the override.
func CheckRefusal(addr string, tokens []string, insecureNoAuth bool) error {
	if len(tokens) > 0 || insecureNoAuth {
		return nil
	}
	host := addr
	if i := strings.LastIndex(addr, ":"); i != -1 {
		host = addr[:i]
	}
	switch host {
	case "", "localhost", "::1", "[::1]":
		return nil
	}
	if strings.HasPrefix(host, "127.") { // all of 127/8 is loopback (Q-10)
		return nil
	}
	return fmt.Errorf("refusing to bind %s without a token (use --token-file, or --insecure-no-auth to override)", addr)
}

// tokenDigest hashes a candidate for constant-time comparison (raw-token
// comparison leaks length — F-022).
func tokenDigest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// authMiddleware enforces bearer auth on all routes except healthz/readyz.
// Tokens never appear in logs or error bodies.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	digests := make([][]byte, 0, len(s.cfg.Tokens))
	for _, t := range s.cfg.Tokens {
		digests = append(digests, tokenDigest(t))
	}
	open := map[string]bool{"/healthz": true, "/readyz": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if open[r.URL.Path] || len(digests) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		presented := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			presented = strings.TrimPrefix(h, "Bearer ")
		} else if q := r.URL.Query().Get("token"); q != "" {
			presented = q
		}
		if !anyTokenMatch(tokenDigest(presented), digests, subtle.ConstantTimeCompare) {
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// anyTokenMatch compares the presented digest against EVERY candidate
// (no early exit — timing must not leak the match position, spec §4).
func anyTokenMatch(pd []byte, digests [][]byte, cmp func(a, b []byte) int) bool {
	ok := 0
	for _, d := range digests {
		ok |= cmp(pd, d)
	}
	return ok == 1
}

// validServeTier reports whether a client-requested compile tier is in the
// bounded set {0..3} that the compiles_total tier label allows (SPEC-07 D3
// cardinality). nil means "use config default" and is always valid.
func validServeTier(t *int) bool {
	if t == nil {
		return true
	}
	return *t >= 0 && *t <= 3
}

// execCompile runs one queued compile through the SHARED serveJobRunner
// (F-051 — the coordinator fence + progress wiring live once, in deps.go).
func (s *Server) execCompile(ctx context.Context, j *Job) (json.RawMessage, error) {
	// SPEC-07 D3 cardinality: the serve queue bypasses the engine's tier
	// validation, so bound the client-requested tier here — otherwise
	// compiles_total{tier=N} ships for any N.
	if !validServeTier(j.Request.Tier) {
		return nil, fmt.Errorf("compile: tier %d out of range (0..3; omit for config default)", *j.Request.Tier)
	}
	opts := compiler.CompileOpts{Ctx: ctx}
	// SPEC-07: a stop-driven cancellation is "interrupted", not "cancelled".
	opts.IsInterrupted = s.queue.Stopped
	if j.Request.Tier != nil {
		opts.Tier = j.Request.Tier
	}
	opts.Model = j.Request.Model
	opts.MaxDocs = j.Request.MaxDocs
	if j.Request.MaxCost != nil {
		cost, err := parseDecimal(*j.Request.MaxCost)
		if err != nil {
			return nil, fmt.Errorf("max_cost %q: %w", *j.Request.MaxCost, err)
		}
		opts.MaxCost = cost
	}
	result, err := NewJobRunner(s.deps, nil).RunCompile(ctx, s.cfg.Workspace, opts)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
