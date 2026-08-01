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
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
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
		pd := tokenDigest(presented)
		ok := 0
		for _, d := range digests {
			// No early exit: every candidate is compared, so timing does
			// not leak the match position (spec §4 structural property).
			ok |= subtle.ConstantTimeCompare(pd, d)
		}
		if ok != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// execCompile runs one queued compile through the SHARED serveJobRunner
// (F-051 — the coordinator fence + progress wiring live once, in deps.go).
func (s *Server) execCompile(ctx context.Context, j *Job) (json.RawMessage, error) {
	opts := compiler.CompileOpts{Ctx: ctx}
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
