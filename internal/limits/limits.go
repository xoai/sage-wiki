// Package limits is the single home for SPEC-08 resource limits and their
// typed errors. It is a leaf package: pkg/engine re-exports the exported
// names (aliases), and internal packages enforce them at their seams.
package limits

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults per SPEC-08 D1. Zero-valued fields resolve to these via Resolve;
// a zero value never means "limit disabled".
const (
	DefaultMaxDocBytes                  int64 = 10 << 20 // 10 MiB
	DefaultMaxDocsPerCaptureBatch       int64 = 10
	DefaultMaxCompileBatch              int64 = 1000
	DefaultMaxQueryBytes                int64 = 32 << 10 // 32 KiB
	DefaultMaxGraphTraversalNodes       int64 = 10000
	DefaultMaxConcurrentProviderCalls   int64 = 20
	DefaultMaxConcurrentRequestsPerConn int64 = 8

	DefaultProviderTimeout   = 120 * time.Second
	DefaultCompileDocTimeout = 15 * time.Minute
)

// Limits holds the enforceable resource caps. Zero fields resolve to the
// package defaults; Resolve returns a fully-populated copy. The yaml tags
// let internal/config unmarshal the workspace `limits:` block directly
// into this struct (one shape, no duplicate config type).
type Limits struct {
	MaxDocBytes                  int64         `yaml:"max_doc_bytes,omitempty"`
	MaxDocsPerCaptureBatch       int64         `yaml:"max_docs_per_capture_batch,omitempty"`
	MaxCompileBatch              int64         `yaml:"max_compile_batch,omitempty"`
	MaxQueryBytes                int64         `yaml:"max_query_bytes,omitempty"`
	MaxGraphTraversalNodes       int64         `yaml:"max_graph_traversal_nodes,omitempty"`
	MaxConcurrentProviderCalls   int64         `yaml:"max_concurrent_provider_calls,omitempty"`
	MaxConcurrentRequestsPerConn int64         `yaml:"max_concurrent_requests_per_conn,omitempty"`
	ProviderTimeout              time.Duration `yaml:"provider_timeout,omitempty"`
	CompileDocTimeout            time.Duration `yaml:"compile_doc_timeout,omitempty"`
}

// parseTimeout accepts "15m", "120s" (time.ParseDuration) or a bare
// integer interpreted as seconds. Empty means unset (zero).
func parseTimeout(field, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("limits: %s: invalid duration %q (use e.g. 120s, 15m, or seconds)", field, s)
}

// UnmarshalYAML implements yaml.Unmarshaler. Two reasons it is custom:
// yaml.v3 cannot decode duration strings into time.Duration, and Load
// unmarshals over Defaults() — so only keys PRESENT in the YAML may
// overwrite the receiver (a blanket decode would zero the defaults).
// Unknown keys are ignored, matching the config loader's lenient style.
func (l *Limits) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.DocumentNode && len(value.Content) > 0 {
		value = value.Content[0]
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("limits: expected a mapping, got yaml node kind %d", value.Kind)
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key, val := value.Content[i].Value, value.Content[i+1]
		switch key {
		case "max_doc_bytes":
			if err := val.Decode(&l.MaxDocBytes); err != nil {
				return fmt.Errorf("limits: max_doc_bytes: %w", err)
			}
		case "max_docs_per_capture_batch":
			if err := val.Decode(&l.MaxDocsPerCaptureBatch); err != nil {
				return fmt.Errorf("limits: max_docs_per_capture_batch: %w", err)
			}
		case "max_compile_batch":
			if err := val.Decode(&l.MaxCompileBatch); err != nil {
				return fmt.Errorf("limits: max_compile_batch: %w", err)
			}
		case "max_query_bytes":
			if err := val.Decode(&l.MaxQueryBytes); err != nil {
				return fmt.Errorf("limits: max_query_bytes: %w", err)
			}
		case "max_graph_traversal_nodes":
			if err := val.Decode(&l.MaxGraphTraversalNodes); err != nil {
				return fmt.Errorf("limits: max_graph_traversal_nodes: %w", err)
			}
		case "max_concurrent_provider_calls":
			if err := val.Decode(&l.MaxConcurrentProviderCalls); err != nil {
				return fmt.Errorf("limits: max_concurrent_provider_calls: %w", err)
			}
		case "max_concurrent_requests_per_conn":
			if err := val.Decode(&l.MaxConcurrentRequestsPerConn); err != nil {
				return fmt.Errorf("limits: max_concurrent_requests_per_conn: %w", err)
			}
		case "provider_timeout":
			var s string
			if err := val.Decode(&s); err != nil {
				return fmt.Errorf("limits: provider_timeout: %w", err)
			}
			d, err := parseTimeout("provider_timeout", s)
			if err != nil {
				return err
			}
			l.ProviderTimeout = d
		case "compile_doc_timeout":
			var s string
			if err := val.Decode(&s); err != nil {
				return fmt.Errorf("limits: compile_doc_timeout: %w", err)
			}
			d, err := parseTimeout("compile_doc_timeout", s)
			if err != nil {
				return err
			}
			l.CompileDocTimeout = d
		}
	}
	return nil
}

// Resolve returns l with every zero field replaced by its default.
func (l Limits) Resolve() Limits {
	if l.MaxDocBytes == 0 {
		l.MaxDocBytes = DefaultMaxDocBytes
	}
	if l.MaxDocsPerCaptureBatch == 0 {
		l.MaxDocsPerCaptureBatch = DefaultMaxDocsPerCaptureBatch
	}
	if l.MaxCompileBatch == 0 {
		l.MaxCompileBatch = DefaultMaxCompileBatch
	}
	if l.MaxQueryBytes == 0 {
		l.MaxQueryBytes = DefaultMaxQueryBytes
	}
	if l.MaxGraphTraversalNodes == 0 {
		l.MaxGraphTraversalNodes = DefaultMaxGraphTraversalNodes
	}
	if l.MaxConcurrentProviderCalls == 0 {
		l.MaxConcurrentProviderCalls = DefaultMaxConcurrentProviderCalls
	}
	if l.MaxConcurrentRequestsPerConn == 0 {
		l.MaxConcurrentRequestsPerConn = DefaultMaxConcurrentRequestsPerConn
	}
	if l.ProviderTimeout == 0 {
		l.ProviderTimeout = DefaultProviderTimeout
	}
	if l.CompileDocTimeout == 0 {
		l.CompileDocTimeout = DefaultCompileDocTimeout
	}
	return l
}

// Which values name the limit a LimitError enforces. They are the `limit`
// label values of the limit_exceeded event/metric.
const (
	WhichDocBytes           = "doc_bytes"
	WhichCaptureBatch       = "capture_batch"
	WhichCompileBatch       = "compile_batch"
	WhichQueryBytes         = "query_bytes"
	WhichGraphTraversal     = "graph_traversal"
	WhichConcurrentProvider = "concurrent_provider_calls"
	WhichConcurrentRequests = "concurrent_requests_per_conn"
	WhichEncoding           = "encoding"
	WhichProviderTimeout    = "provider_timeout"
	WhichCompileDocTimeout  = "compile_doc_timeout"
	WhichInvalidName        = "invalid_name"
)

// Sentinels live here (leaf package) so LimitError can Unwrap without an
// import cycle. pkg/engine re-exports them as aliases — errors.Is
// back-compat holds by variable identity.
var (
	// ErrDocTooLarge reports a document over the size cap.
	ErrDocTooLarge = errors.New("engine: document too large")
	// ErrBatchTooLarge reports a batch over its count cap.
	ErrBatchTooLarge = errors.New("engine: batch too large")
	// ErrQueryTooLarge reports a query over the byte cap.
	ErrQueryTooLarge = errors.New("engine: query too large")
	// ErrTraversalTooWide reports a graph traversal over the node cap.
	ErrTraversalTooWide = errors.New("engine: graph traversal too wide")
	// ErrEncoding reports content rejected by the encoding gate.
	ErrEncoding = errors.New("engine: unsupported encoding")
	// ErrTimeout reports a provider or per-doc compile timeout.
	ErrTimeout = errors.New("engine: timeout")
	// ErrInvalidName reports a name/label rejected by charset or
	// sanitize-then-check predicates (SPEC-08 D3).
	ErrInvalidName = errors.New("engine: invalid name")
)

// sentinelFor maps a Which value to the sentinel LimitError unwraps to.
// Unknown whens unwrap to nil (still a typed LimitError, no sentinel).
func sentinelFor(which string) error {
	switch which {
	case WhichDocBytes:
		return ErrDocTooLarge
	case WhichCaptureBatch, WhichCompileBatch:
		return ErrBatchTooLarge
	case WhichQueryBytes:
		return ErrQueryTooLarge
	case WhichGraphTraversal:
		return ErrTraversalTooWide
	case WhichEncoding:
		return ErrEncoding
	case WhichProviderTimeout, WhichCompileDocTimeout:
		return ErrTimeout
	default:
		return nil
	}
}

// LimitError is the typed error every limit violation returns. It carries
// the data operators need (which limit, its value, the offending value) and
// unwraps to the matching sentinel so errors.Is keeps working.
type LimitError struct {
	Which string
	Limit int64
	Got   int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("engine: limit exceeded: %s (limit %d, got %d)",
		e.Which, e.Limit, e.Got)
}

// Unwrap returns the sentinel for e.Which, or nil for unknown whens.
func (e *LimitError) Unwrap() error {
	return sentinelFor(e.Which)
}

// New is shorthand for constructing a LimitError at enforcement sites.
func New(which string, limit, got int64) *LimitError {
	return &LimitError{Which: which, Limit: limit, Got: got}
}
