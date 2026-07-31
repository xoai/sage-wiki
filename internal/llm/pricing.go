package llm

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// PriceSource identifies where a registry entry came from.
const (
	PriceSourceBuiltin = "builtin"
	PriceSourceUser    = "user"
	// PriceSourceProviderAPI is reserved for providers that report pricing
	// via an API. No provider does today — no loader emits this source.
	PriceSourceProviderAPI = "provider-api"
)

// Price holds per-million-token pricing for one provider:model. A nil
// component means that component's price is unknown — unknown components
// make the whole cost unknown (never zero, never guessed).
type Price struct {
	InputPerMTok       *decimal.Decimal
	CachedInputPerMTok *decimal.Decimal
	CacheWritePerMTok  *decimal.Decimal
	OutputPerMTok      *decimal.Decimal
	BatchInputPerMTok  *decimal.Decimal
	BatchOutputPerMTok *decimal.Decimal
	AsOf               time.Time
	Source             string
}

// RegistryEntry is one key + price, returned by Entries for display/audit.
type RegistryEntry struct {
	Key   string // "provider:model"
	Price Price
}

// Registry is a provider:model keyed price lookup. It never falls back
// across providers — a model unknown under its own provider is unknown.
type Registry struct {
	entries map[string]Price
	prefix  map[string][]string // provider → sorted model keys for prefix match
}

//go:embed prices/default.json
var embeddedDefaultPrices []byte

// defaultPricesJSON returns the embedded builtin price document.
func defaultPricesJSON() ([]byte, error) {
	if len(embeddedDefaultPrices) == 0 {
		return nil, fmt.Errorf("embedded prices/default.json is empty")
	}
	return embeddedDefaultPrices, nil
}

// jsonPriceEntry is the on-disk shape of one registry entry. Decimals are
// strings for exactness; empty string = unknown component.
type jsonPriceEntry struct {
	Input       string `json:"input"`
	CachedInput string `json:"cached_input"`
	CacheWrite  string `json:"cache_write_input"`
	Output      string `json:"output"`
	BatchInput  string `json:"batch_input"`
	BatchOutput string `json:"batch_output"`
	AsOf        string `json:"as_of"`
}

type jsonPriceDoc struct {
	Comment string                    `json:"_comment"`
	Prices  map[string]jsonPriceEntry `json:"prices"`
}

// LoadRegistry builds the effective registry: embedded defaults, then the
// user file (~/.sage-wiki/prices.json), then the workspace price table
// (legacy PERF-04 shape or the registry shape) — later wins. Missing files
// are skipped; malformed files are a hard error.
func LoadRegistry(workspaceTablePath string) (*Registry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate user home for price registry: %w", err)
	}
	return loadRegistryFrom(workspaceTablePath, filepath.Join(home, ".sage-wiki", "prices.json"))
}

func loadRegistryFrom(workspaceTablePath, userPath string) (*Registry, error) {
	r := &Registry{entries: make(map[string]Price), prefix: make(map[string][]string)}

	if err := r.loadBuiltin(); err != nil {
		return nil, err
	}
	if err := r.loadUserFile(userPath); err != nil {
		return nil, err
	}
	if workspaceTablePath != "" {
		if err := r.loadWorkspaceFile(workspaceTablePath); err != nil {
			return nil, err
		}
	}
	r.rebuildPrefix()
	return r, nil
}

func (r *Registry) loadBuiltin() error {
	raw, err := defaultPricesJSON()
	if err != nil {
		return err
	}
	var doc jsonPriceDoc
	if err := unmarshalJSON(raw, &doc); err != nil {
		return fmt.Errorf("embedded prices/default.json malformed: %w", err)
	}
	for key, e := range doc.Prices {
		p, err := e.toPrice(PriceSourceBuiltin)
		if err != nil {
			return fmt.Errorf("embedded prices %q: %w", key, err)
		}
		r.entries[key] = p
	}
	return nil
}

func (r *Registry) loadUserFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read user price registry %s: %w", path, err)
	}
	var doc jsonPriceDoc
	if err := unmarshalJSON(raw, &doc); err != nil {
		return fmt.Errorf("user price registry %s malformed: %w", path, err)
	}
	for key, e := range doc.Prices {
		if !strings.Contains(key, ":") {
			return fmt.Errorf("user price registry %s: key %q must be provider:model", path, key)
		}
		p, err := e.toPrice(PriceSourceUser)
		if err != nil {
			return fmt.Errorf("user price registry %s %q: %w", path, key, err)
		}
		r.entries[key] = p
	}
	return nil
}

// loadWorkspaceFile accepts both the registry shape ({"prices": {...}}) and
// the legacy PERF-04 shape ({"provider": {"model": {float fields}}}).
func (r *Registry) loadWorkspaceFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read workspace price table %s: %w", path, err)
	}

	var doc jsonPriceDoc
	if err := unmarshalJSON(raw, &doc); err == nil && len(doc.Prices) > 0 {
		for key, e := range doc.Prices {
			if !strings.Contains(key, ":") {
				return fmt.Errorf("workspace price table %s: key %q must be provider:model", path, key)
			}
			p, err := e.toPrice(PriceSourceUser)
			if err != nil {
				return fmt.Errorf("workspace price table %s %q: %w", path, key, err)
			}
			r.entries[key] = p
		}
		return nil
	}

	var legacy map[string]map[string]ModelPrice
	if err := unmarshalJSON(raw, &legacy); err != nil {
		return fmt.Errorf("workspace price table %s malformed (neither registry nor legacy PERF-04 shape): %w", path, err)
	}
	for provider, models := range legacy {
		for model, mp := range models {
			r.entries[provider+":"+model] = legacyToPrice(mp)
		}
	}
	return nil
}

func legacyToPrice(mp ModelPrice) Price {
	return Price{
		InputPerMTok:       floatPtr(mp.Input),
		CachedInputPerMTok: floatPtr(mp.CachedInput),
		OutputPerMTok:      floatPtr(mp.Output),
		BatchInputPerMTok:  floatPtr(mp.BatchInput),
		BatchOutputPerMTok: floatPtr(mp.BatchOutput),
		Source:             PriceSourceUser,
	}
}

// floatPtr maps a legacy PERF-04 float field to a decimal pointer. The
// PERF-04 convention is 0 = UNSET (the table shape omits zero prices), so
// 0.0 maps to nil = unknown — a deliberately-free $0 component is not
// representable in the legacy shape.
func floatPtr(v float64) *decimal.Decimal {
	if v == 0 {
		return nil
	}
	d := decimal.NewFromFloat(v)
	return &d
}

func (e jsonPriceEntry) toPrice(source string) (Price, error) {
	p := Price{Source: source}
	var err error
	if p.InputPerMTok, err = decPtr(e.Input); err != nil {
		return p, fmt.Errorf("input: %w", err)
	}
	if p.CachedInputPerMTok, err = decPtr(e.CachedInput); err != nil {
		return p, fmt.Errorf("cached_input: %w", err)
	}
	if p.CacheWritePerMTok, err = decPtr(e.CacheWrite); err != nil {
		return p, fmt.Errorf("cache_write_input: %w", err)
	}
	if p.OutputPerMTok, err = decPtr(e.Output); err != nil {
		return p, fmt.Errorf("output: %w", err)
	}
	if p.BatchInputPerMTok, err = decPtr(e.BatchInput); err != nil {
		return p, fmt.Errorf("batch_input: %w", err)
	}
	if p.BatchOutputPerMTok, err = decPtr(e.BatchOutput); err != nil {
		return p, fmt.Errorf("batch_output: %w", err)
	}
	if e.AsOf != "" {
		p.AsOf, err = time.Parse("2006-01-02", e.AsOf)
		if err != nil {
			return p, fmt.Errorf("as_of %q: %w", e.AsOf, err)
		}
	}
	return p, nil
}

func decPtr(s string) (*decimal.Decimal, error) {
	if s == "" {
		return nil, nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *Registry) rebuildPrefix() {
	r.prefix = make(map[string][]string)
	for key := range r.entries {
		provider, model, _ := strings.Cut(key, ":")
		r.prefix[provider] = append(r.prefix[provider], model)
	}
	for provider := range r.prefix {
		sort.Strings(r.prefix[provider])
	}
}

// Lookup resolves provider:model exactly, then by longest prefix within the
// same provider where the KEY is a prefix of the model (versioned models
// like gpt-4o-2024-08-06 → gpt-4o). The reverse direction (model shorter
// than the key) never matches: a bare "gpt" must not inherit gpt-4o-mini's
// price. It never resolves across providers: unknown under the given
// provider is unknown, full stop.
func (r *Registry) Lookup(provider, model string) (*Price, bool) {
	if p, ok := r.entries[provider+":"+model]; ok {
		return &p, true
	}
	best := ""
	for _, name := range r.prefix[provider] {
		if strings.HasPrefix(model, name) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return nil, false
	}
	p := r.entries[provider+":"+best]
	return &p, true
}

// Entries returns all registry entries sorted by key (deterministic output).
func (r *Registry) Entries() []RegistryEntry {
	out := make([]RegistryEntry, 0, len(r.entries))
	for key, p := range r.entries {
		out = append(out, RegistryEntry{Key: key, Price: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// unmarshalJSON is a seam for tests and keeps a single json.Unmarshal call
// site for the registry shapes.
func unmarshalJSON(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}
