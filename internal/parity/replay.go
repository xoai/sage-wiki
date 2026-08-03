// Package parity implements the SPEC-09 golden-corpus parity suite:
// record/replay HTTP seam, workspace builder, and the byte/graph/search/
// round-trip golden checks.
package parity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Fixture is one recorded request→response pair.
type Fixture struct {
	Request  CanonicalRequest  `json:"request"`
	Response CanonicalResponse `json:"response"`
}

// CanonicalRequest is the stable request form the fixture key hashes.
type CanonicalRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"` // canonical JSON (keys sorted, compact)
}

// CanonicalResponse is the stored origin response.
type CanonicalResponse struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// canonicalKey hashes method+path+canonical-body into the fixture name.
// RFC3339 timestamps in the body are sentinel-replaced first: compiled
// artifacts carry wall-clock `created_at` until SPEC-04 lands, and
// fixtures must match across different compile times. The recorded
// response (computed from the timestamped text at record time) is the
// STABLE value goldens compare against. Sampling parameters (temperature,
// top-level and provider-nested) are stripped: request matching must not
// depend on them — SPEC-04 D2 makes temperature:0 explicit on the wire and
// every committed fixture predates it.
func canonicalKey(method, path string, body []byte) (string, error) {
	canon, err := canonicalJSON(stripSamplingParams(rfc3339Re.ReplaceAll(body, []byte("<TS>"))))
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte(method+" "+path+"\n"), canon...))
	return hex.EncodeToString(h[:]), nil
}

// stripSamplingParams removes temperature from the top level and from a
// nested generationConfig object (gemini shape). Non-JSON bodies pass
// through untouched.
func stripSamplingParams(body []byte) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return body
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return body
	}
	delete(obj, "temperature")
	if gc, ok := obj["generationConfig"].(map[string]any); ok {
		delete(gc, "temperature")
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// canonicalJSON produces the stable form: parsed and re-marshaled (Go's
// encoding/json sorts object keys) with whitespace dropped. Non-JSON
// bodies pass through verbatim.
func canonicalJSON(body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return []byte{}, nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("canonicalize non-JSON request body: %w", err)
	}
	return json.Marshal(v)
}

// Server is a record/replay OpenAI-compatible stub (SPEC-09 §2.2).
type Server struct {
	*httptest.Server
	dir    string
	origin string // record mode only
}

// NewReplayServer serves committed fixtures from dir; a cache miss is a
// hard error carrying the unmatched request's canonical JSON.
func NewReplayServer(dir string) (*Server, error) {
	s := &Server{dir: dir}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handleReplay))
	return s, nil
}

// NewRecordServer proxies originURL, writing each 2xx exchange as a
// fixture under dir. The forward honors the incoming request's context
// and applies a 30s outbound timeout (AGENTS.md rule 7).
func NewRecordServer(originURL, dir string) (*Server, error) {
	s := &Server{dir: dir, origin: strings.TrimSuffix(originURL, "/")}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handleRecord))
	return s, nil
}

// URL returns the base URL to point api.base_url at.
func (s *Server) URL() string { return s.Server.URL }

// Close shuts the stub down.
func (s *Server) Close() { s.Server.Close() }

var errReplayMiss = fmt.Errorf("replay cache miss")

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	key, err := canonicalKey(r.Method, r.URL.Path, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, key+".json"))
	if err != nil {
		miss := replayMissError(key, r.Method, r.URL.Path, body, s.dir)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTeapot)
		fmt.Fprint(w, miss)
		return
	}
	var fx Fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		http.Error(w, "corrupt fixture "+key+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(fx.Response.Status)
	w.Write(fx.Response.Body)
}

// replayMissError formats the failure the suite surfaces verbatim.
func replayMissError(key, method, path string, body []byte, dir string) string {
	canon, _ := canonicalJSON(body)
	var known []string
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		return fmt.Sprintf("replay cache miss: %s %s\nkey: %s\n(request canonicalization ok; fixture dir unreadable: %v)\n",
			method, path, key, readErr)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			known = append(known, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(known)
	short := known
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("replay cache miss: %s %s\nkey: %s\nrequest: %s\nknown fixture keys (first %d of %d): %s\n",
		method, path, key, canon, len(short), len(known), strings.Join(short, ", "))
}

// RecordMiss reports whether an HTTP response is a replay miss (418).
func RecordMiss(status int) bool { return status == http.StatusTeapot }

func (s *Server) handleRecord(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	fwd, err := http.NewRequestWithContext(ctx, r.Method, s.origin+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	fwd.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(fwd)
	if err != nil {
		http.Error(w, "origin unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		key, err := canonicalKey(r.Method, r.URL.Path, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		canon, _ := canonicalJSON(body)
		fx := Fixture{
			Request:  CanonicalRequest{Method: r.Method, Path: r.URL.Path, Body: canon},
			Response: CanonicalResponse{Status: resp.StatusCode, Body: respBody},
		}
		out, err := json.MarshalIndent(fx, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.MkdirAll(s.dir, 0o755); err == nil {
			// tmp+rename: a crash mid-write must not leave a truncated
			// fixture that later reads as checked-in corruption.
			tmp := filepath.Join(s.dir, key+".json.tmp")
			if werr := os.WriteFile(tmp, out, 0o644); werr != nil {
				http.Error(w, "write fixture: "+werr.Error(), http.StatusInternalServerError)
				return
			}
			if werr := os.Rename(tmp, filepath.Join(s.dir, key+".json")); werr != nil {
				http.Error(w, "commit fixture: "+werr.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}
