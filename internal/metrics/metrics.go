// Package metrics is the zero-dependency observability registry (P2-2):
// counters, gauges, fixed-bucket histograms, a log Snapshot, and a
// hand-rolled Prometheus text exposition handler. Always live — the only
// gated surface is the HTTP endpoint (serve.metrics). Every instrument is
// nil-safe: recording on a nil handle is a no-op, so hook sites never
// branch. Series register lazily at FIRST RECORD — zero-activity series
// never appear in exposition or snapshots (design D8).
//
// Cardinality discipline (design D3): label keys/values must come from the
// pinned inventory — pass, stage, direction, provider, cache. The runtime
// enumeration test enforces it.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Bucket sets (package constants — tunable in code, design D7).
var (
	latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	compileBuckets = []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120, 300}
)

// LatencyBuckets returns the default search/query histogram buckets.
func LatencyBuckets() []float64 { return append([]float64{}, latencyBuckets...) }

// CompileBuckets returns the default compile-pass histogram buckets.
func CompileBuckets() []float64 { return append([]float64{}, compileBuckets...) }

type series struct {
	name      string
	labels    string   // pre-rendered `{k="v",...}` or ""
	rawLabels []string // raw k,v pairs at creation (ValidateLabels uses these — rendered re-parse would break on values containing , or =)
	help      string
	typ       string
	counter   *Counter
	gauge     *Gauge
	hist      *Histogram
}

var registry = struct {
	sync.Mutex
	handles map[string]*series // name+labels key → series (handle identity)
	order   map[string]bool    // registered (≥1 record) series
}{handles: map[string]*series{}, order: map[string]bool{}}

// ResetForTest clears the registry (tests only, any package).
func ResetForTest() {
	resetRegistry()
}

// resetRegistry clears the registry (tests only).
func resetRegistry() {
	registry.Lock()
	defer registry.Unlock()
	registry.handles = map[string]*series{}
	registry.order = map[string]bool{}
}

func labelsKey(name string, labels []string) string {
	return name + "\x00" + strings.Join(labels, "\x01")
}

func renderLabels(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(labels); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(labels[i])
		b.WriteString(`="`)
		b.WriteString(escapeLabel(labels[i+1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func getSeries(name, typ string, labels []string, mk func() any) *series {
	key := typ + "|" + labelsKey(name, labels) // type is part of identity: a counter/gauge name collision must not no-op
	registry.Lock()
	defer registry.Unlock()
	if s, ok := registry.handles[key]; ok {
		return s
	}
	s := &series{name: name, labels: renderLabels(labels), rawLabels: append([]string{}, labels...), typ: typ}
	switch v := mk().(type) {
	case *Counter:
		v.self = s
		s.counter = v
	case *Gauge:
		v.self = s
		s.gauge = v
	case *Histogram:
		v.self = s
		s.hist = v
	}
	registry.handles[key] = s
	return s
}

// register marks a series as having ≥1 recording (lazy registration).
func register(s *series) {
	registry.Lock()
	registry.order[s.typ+"|"+s.name+s.labels] = true
	registry.Unlock()
}

// Counter is a monotonically increasing integer series.
type Counter struct {
	v    atomic.Int64
	self *series
}

// Inc adds 1. Nil-safe.
func (c *Counter) Inc() { c.Add(1) }

// Add adds n. Nil-safe.
func (c *Counter) Add(n int64) {
	if c == nil {
		return
	}
	c.v.Add(n)
	register(c.self)
}

// Gauge is a point-in-time integer series.
type Gauge struct {
	v    atomic.Int64
	self *series
}

// Set sets the gauge. Nil-safe.
func (g *Gauge) Set(v int64) {
	if g == nil {
		return
	}
	g.v.Store(v)
	register(g.self)
}

// Inc adds 1. Nil-safe.
func (g *Gauge) Inc() { g.Add(1) }

// Dec subtracts 1. Nil-safe.
func (g *Gauge) Dec() { g.Add(-1) }

// Add adds n. Nil-safe.
func (g *Gauge) Add(n int64) {
	if g == nil {
		return
	}
	g.v.Add(n)
	register(g.self)
}

// Histogram is a fixed-bucket latency distribution.
type Histogram struct {
	buckets []float64
	counts  []atomic.Int64
	sum     atomic.Int64 // nanoseconds
	count   atomic.Int64
	self    *series
}

// Observe records a duration in seconds. Nil-safe. count/sum are bumped
// BEFORE the bucket so a concurrent exposition never emits a non-monotonic
// histogram (+Inf below a finite bucket — Prometheus drops those).
func (h *Histogram) Observe(seconds float64) {
	if h == nil {
		return
	}
	h.count.Add(1)
	h.sum.Add(int64(seconds * 1e9))
	for i, b := range h.buckets {
		if seconds <= b {
			h.counts[i].Add(1)
			break
		}
	}
	register(h.self)
}

// ObserveDuration records the time since start. Nil-safe.
func ObserveDuration(h *Histogram, start time.Time) {
	if h == nil {
		return
	}
	h.Observe(time.Since(start).Seconds())
}

// CounterNamed returns the (single-identity) counter for name+labels.
// labels are k,v pairs from the pinned inventory; odd trailing keys are
// ignored (programming error — the inventory test catches them).
func CounterNamed(name string, labels ...string) *Counter {
	return getSeries(name, "counter", labels, func() any { return &Counter{} }).counter
}

// GaugeNamed returns the gauge for name+labels.
func GaugeNamed(name string, labels ...string) *Gauge {
	return getSeries(name, "gauge", labels, func() any { return &Gauge{} }).gauge
}

// HistogramNamed returns the histogram for name+labels with the given
// buckets (first call wins).
func HistogramNamed(name string, buckets []float64, labels ...string) *Histogram {
	return getSeries(name, "histogram", labels, func() any {
		return &Histogram{buckets: buckets, counts: make([]atomic.Int64, len(buckets))}
	}).hist
}

// Snapshot returns alternating key/value log args for every registered
// series, or nil when the registry has no recordings (design: empty
// snapshot emits no log line). Histograms appear as name_count/name_sum
// (seconds, float).
func Snapshot() []any {
	fams := sortedFamilies()
	var out []any
	for _, s := range fams {
		if !registered(s) {
			continue
		}
		key := s.name + s.labels // snapshot key: labels in the key, never in values
		switch s.typ {
		case "counter":
			out = append(out, key, s.counter.v.Load())
		case "gauge":
			out = append(out, key, s.gauge.v.Load())
		case "histogram":
			out = append(out, key+"_count", s.hist.count.Load())
			out = append(out, key+"_sum", float64(s.hist.sum.Load())/1e9)
		}
	}
	return out
}

func sortedFamilies() []*series {
	registry.Lock()
	defer registry.Unlock()
	var fams []*series
	for _, s := range registry.handles {
		fams = append(fams, s)
	}
	sort.Slice(fams, func(i, j int) bool {
		if fams[i].name != fams[j].name {
			return fams[i].name < fams[j].name
		}
		return fams[i].labels < fams[j].labels
	})
	return fams
}

func f64(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// Handler returns the Prometheus text exposition handler (valid on an
// empty registry: 200 with no series lines).
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		var b strings.Builder
		seenHelp := map[string]bool{}
		for _, s := range sortedFamilies() {
			if !registered(s) {
				continue
			}
			if !seenHelp[s.name] {
				help := helpText(s.name)
				fmt.Fprintf(&b, "# HELP %s %s\n", s.name, help)
				fmt.Fprintf(&b, "# TYPE %s %s\n", s.name, s.typ)
				seenHelp[s.name] = true
			}
			switch s.typ {
			case "counter":
				fmt.Fprintf(&b, "%s%s %s\n", s.name, s.labels, strconv.FormatInt(s.counter.v.Load(), 10))
			case "gauge":
				fmt.Fprintf(&b, "%s%s %s\n", s.name, s.labels, strconv.FormatInt(s.gauge.v.Load(), 10))
			case "histogram":
				var cum int64
				for i, bk := range s.hist.buckets {
					cum += s.hist.counts[i].Load()
					fmt.Fprintf(&b, "%s_bucket%s %d\n", s.name, leLabel(s.labels, f64(bk)), cum)
				}
				fmt.Fprintf(&b, "%s_bucket%s %d\n", s.name, leLabel(s.labels, "+Inf"), s.hist.count.Load())
				fmt.Fprintf(&b, "%s_sum%s %s\n", s.name, s.labels, f64(float64(s.hist.sum.Load())/1e9))
				fmt.Fprintf(&b, "%s_count%s %d\n", s.name, s.labels, s.hist.count.Load())
			}
		}
		if _, err := w.Write([]byte(b.String())); err != nil {
			return
		}
	})
}

func leLabel(labels, le string) string {
	if labels == "" {
		return fmt.Sprintf(`{le="%s"}`, le)
	}
	return strings.TrimSuffix(labels, "}") + fmt.Sprintf(`,le="%s"}`, le)
}

func registered(s *series) bool {
	registry.Lock()
	defer registry.Unlock()
	return registry.order[s.typ+"|"+s.name+s.labels]
}

// helpText derives a HELP line; families without an entry get a
// placeholder (Prometheus requires the line, spec §6).
var helpTexts = map[string]string{
	"compile_pass_duration_seconds":   "Compile pass wall-clock duration in seconds.",
	"llm_tokens_total":                "LLM tokens by provider, model, pass, and direction.",
	"llm_retries_total":               "LLM retry attempts.",
	"llm_rate_limited_total":          "LLM HTTP 429 responses.",
	"compile_backpressure_limit":      "Current backpressure concurrency limit.",
	"compile_backpressure_in_flight":  "Current backpressure in-flight count.",
	"search_duration_seconds":         "Search stage latency in seconds.",
	"search_channel_duration_seconds": "Search channel leg latency in seconds.",
	"query_duration_seconds":          "End-to-end query latency in seconds.",
	"embed_calls_total":               "Embedding API calls.",
	"vector_cache_hits_total":         "Vector cache searches served from the loaded matrix.",
	"vector_cache_misses_total":       "Vector cache reloads triggered.",
	// SPEC-07 series.
	"compiles_total":           "Compile jobs by tier and outcome.",
	"compile_duration_seconds": "End-to-end compile job duration in seconds.",
	"workspaces_open":          "Currently open workspaces (engine Manager).",
	"job_queue_depth":          "Pending jobs in the serve compile queue.",
	"events_dropped_total":     "Events dropped by bounded event buffers.",
	"mirror_ship_lag_seconds":  "Seconds since the last successful mirror ship pass.",
	// SPEC-08 series.
	"limit_exceeded_total": "Resource-limit rejections by limit name (SPEC-08).",
	"edge_rejected_total":  "LLM-emitted edges dropped by span verification (SPEC-08).",
}

func helpText(name string) string {
	if h, ok := helpTexts[name]; ok {
		return h
	}
	return "auto-generated"
}

// allowedLabelKV is the D3 cardinality inventory — the ONLY permitted
// label keys and their permitted values.
var allowedLabelKV = map[string]map[string]bool{
	"pass": {"summarize": true, "extract": true, "write": true, "triples": true, "resolve": true, "communities": true},
	// "total" is the request-scoped end-to-end stage the search facade
	// emits (search.Run); the three others are per-leg.
	"stage":     {"bm25": true, "vector": true, "rrf": true, "total": true},
	"direction": {"input": true, "output": true, "cached": true},
	"cache":     {"doc": true, "chunk": true},
	// provider values come from the config enum and are validated by key only.
	"provider": nil,
	// SPEC-07 additions.
	"tier":    {"0": true, "1": true, "2": true, "3": true},
	"outcome": {"completed": true, "failed": true, "interrupted": true, "cancelled": true},
	"channel": {"bm25": true, "vector": true, "graph": true},
	// model names are provider-defined and unbounded — validated by key
	// only, exactly like provider.
	"model": nil,
	// SPEC-08 additions. "limit" values mirror internal/limits Which
	// constants; "reason" values are pinned in SPEC-08 spec D2.
	"limit": {
		"doc_bytes": true, "capture_batch": true, "compile_batch": true,
		"query_bytes": true, "graph_traversal": true,
		"concurrent_provider_calls": true, "concurrent_requests_per_conn": true,
		"encoding": true, "provider_timeout": true, "compile_doc_timeout": true,
		"invalid_name": true,
	},
	"reason": {"span_missing": true},
}

// ValidateLabels returns an error for every registered series whose labels
// fall outside the D3 inventory. Runtime counterpart to the static
// convention (spec §7.2) — called from hook packages' tests.
func ValidateLabels() error {
	var errs []string
	for _, s := range sortedFamilies() {
		for i := 0; i+1 < len(s.rawLabels); i += 2 {
			key, v := s.rawLabels[i], s.rawLabels[i+1]
			vals, ok := allowedLabelKV[key]
			if !ok {
				errs = append(errs, fmt.Sprintf("%s: label key %q outside inventory", s.name, key))
				continue
			}
			if vals != nil && !vals[v] {
				errs = append(errs, fmt.Sprintf("%s: label value %q for %q outside inventory", s.name, v, key))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("metrics: label inventory violations:\n%s", strings.Join(errs, "\n"))
	}
	return nil
}
