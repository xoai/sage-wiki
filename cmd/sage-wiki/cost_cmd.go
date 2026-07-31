package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/spf13/cobra"

	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/llm"
)

var costCmd = &cobra.Command{
	Use:   "cost",
	Short: "Cost reporting from the usage ledger",
}

var costReportSince string

var costReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Summarize recorded LLM spend by model and pass/tier",
	RunE:  runCostReport,
}

var costModelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List the effective price registry with sources",
	RunE:  runCostModels,
}

func init() {
	costReportCmd.Flags().StringVar(&costReportSince, "since", "", "Only include events after this time (RFC3339 date/timestamp or Go duration like 720h)")
	costCmd.AddCommand(costReportCmd, costModelsCmd)
}

// costRow is one aggregation bucket of the usage ledger.
type costRow struct {
	Provider    string
	Model       string
	Pass        string
	Tier        int
	Input       int
	Cached      int
	CacheWrite  int
	Output      int
	Calls       int
	Cost        *decimal.Decimal // nil when any event in the bucket is unknown
	PriceSource string
}

func runCostReport(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	var since time.Time
	if costReportSince != "" {
		var err error
		since, err = parseSince(costReportSince)
		if err != nil {
			return cli.CLIError(outputFormat, err)
		}
	}

	events, err := llm.ReadUsageLog(llm.NewFileRecorder(dir).Path())
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	byModel, byPass := aggregateUsage(events, since)

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]any{
			"events":   len(events),
			"by_model": jsonRows(byModel),
			"by_pass":  jsonRows(byPass),
		}, ""))
		return nil
	}

	fmt.Print(formatCostReportText(byModel, byPass, since, len(events)))
	return nil
}

// parseSince accepts an RFC3339 timestamp, a YYYY-MM-DD date, or a Go
// duration interpreted as "that long ago".
func parseSince(s string) (time.Time, error) {
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts, nil
	}
	if ts, err := time.Parse("2006-01-02", s); err == nil {
		return ts, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("--since %q is not an RFC3339 timestamp, YYYY-MM-DD date, or Go duration", s)
}

// costBucket accumulates one aggregation bucket. Any unknown-cost event
// poisons the dollar total (unknown is never summed into a fabricated
// partial); token totals stay exact. Mixed price sources render as "mixed"
// rather than silently attributing all spend to the first event's source.
type costBucket struct {
	cost    decimal.Decimal
	unknown bool
	sources map[string]bool
}

func (b *costBucket) add(ev llm.UsageEvent) {
	if b.sources == nil {
		b.sources = map[string]bool{}
	}
	if ev.PriceSource != "" {
		b.sources[ev.PriceSource] = true
	}
	if ev.Cost == nil {
		b.unknown = true
		return
	}
	b.cost = b.cost.Add(*ev.Cost)
}

func (b *costBucket) total() *decimal.Decimal {
	if b.unknown {
		return nil
	}
	c := b.cost
	return &c
}

func (b *costBucket) source() string {
	if len(b.sources) == 1 {
		for s := range b.sources {
			return s
		}
	}
	if len(b.sources) > 1 {
		return "mixed"
	}
	return ""
}

// aggregateUsage buckets events by provider:model and by pass/tier.
// Both slices are sorted for deterministic output.
func aggregateUsage(events []llm.UsageEvent, since time.Time) (byModel, byPass []costRow) {
	type bucket struct {
		row costRow
		agg costBucket
	}
	modelBuckets := map[string]*bucket{}
	passBuckets := map[string]*bucket{}
	for _, ev := range events {
		if !since.IsZero() && ev.TS.Before(since) {
			continue
		}
		mk := ev.Provider + ":" + ev.Model
		mb, ok := modelBuckets[mk]
		if !ok {
			mb = &bucket{row: costRow{Provider: ev.Provider, Model: ev.Model}}
			modelBuckets[mk] = mb
		}
		pk := fmt.Sprintf("%s/%d", ev.Pass, ev.Tier)
		pb, ok := passBuckets[pk]
		if !ok {
			pb = &bucket{row: costRow{Pass: ev.Pass, Tier: ev.Tier}}
			passBuckets[pk] = pb
		}
		for _, b := range []*bucket{mb, pb} {
			b.row.Input += ev.InputTokens
			b.row.Cached += ev.CachedTokens
			b.row.CacheWrite += ev.CacheWriteTokens
			b.row.Output += ev.OutputTokens
			b.row.Calls++
			b.agg.add(ev)
		}
	}
	collect := func(m map[string]*bucket, less func(a, b costRow) bool) []costRow {
		rows := make([]costRow, 0, len(m))
		for _, b := range m {
			r := b.row
			r.Cost = b.agg.total()
			r.PriceSource = b.agg.source()
			rows = append(rows, r)
		}
		sort.Slice(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
		return rows
	}
	byModel = collect(modelBuckets, func(a, b costRow) bool {
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	byPass = collect(passBuckets, func(a, b costRow) bool {
		if a.Pass != b.Pass {
			return a.Pass < b.Pass
		}
		return a.Tier < b.Tier
	})
	return byModel, byPass
}

type jsonCostRow struct {
	Provider    string  `json:"provider,omitempty"`
	Model       string  `json:"model,omitempty"`
	Pass        string  `json:"pass,omitempty"`
	Tier        *int    `json:"tier,omitempty"`
	Calls       int     `json:"calls"`
	Input       int     `json:"input_tokens"`
	Cached      int     `json:"cached_tokens"`
	CacheWrite  int     `json:"cache_write_tokens"`
	Output      int     `json:"output_tokens"`
	Cost        *string `json:"cost"` // null when unknown
	PriceSource string  `json:"price_source,omitempty"`
}

func jsonRows(rows []costRow) []jsonCostRow {
	out := make([]jsonCostRow, 0, len(rows))
	for _, r := range rows {
		jr := jsonCostRow{
			Provider: r.Provider, Model: r.Model, Pass: r.Pass,
			Calls: r.Calls, Input: r.Input, Cached: r.Cached,
			CacheWrite: r.CacheWrite, Output: r.Output, PriceSource: r.PriceSource,
		}
		if r.Pass != "" {
			tier := r.Tier
			jr.Tier = &tier
		}
		if r.Cost != nil {
			s := r.Cost.StringFixed(4)
			jr.Cost = &s
		}
		out = append(out, jr)
	}
	return out
}

func formatCostReportText(byModel, byPass []costRow, since time.Time, totalEvents int) string {
	var b strings.Builder
	b.WriteString("💰 Cost report (from recorded usage)\n")
	if !since.IsZero() {
		b.WriteString(fmt.Sprintf("   Since: %s\n", since.Format(time.RFC3339)))
	}
	if totalEvents == 0 {
		b.WriteString("   No usage recorded yet (.sage/usage.jsonl is empty or absent).\n")
		return b.String()
	}

	b.WriteString("\n   By model:\n")
	writeRows(&b, byModel, func(r costRow) string { return r.Provider + ":" + r.Model })

	tierLabel := func(t int) string {
		if t == llm.TierNotCompileScoped {
			return "-"
		}
		return fmt.Sprintf("%d", t)
	}
	b.WriteString("\n   By pass/tier:\n")
	writeRows(&b, byPass, func(r costRow) string { return fmt.Sprintf("%s (tier %s)", r.Pass, tierLabel(r.Tier)) })
	return b.String()
}

func writeRows(b *strings.Builder, rows []costRow, name func(costRow) string) {
	if len(rows) == 0 {
		b.WriteString("     (none)\n")
		return
	}
	for _, r := range rows {
		costStr := "unknown"
		if r.Cost != nil {
			costStr = "~$" + r.Cost.StringFixed(4)
		}
		fmt.Fprintf(b, "     %-40s %6d calls  %9d in / %9d out  %10s\n",
			name(r), r.Calls, r.Input, r.Output, costStr)
	}
}

func runCostModels(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	// The workspace price table path comes from config; a config that fails
	// to load means the workspace overlay is NOT applied — say so loudly
	// rather than present a silently incomplete audit view.
	tablePath := ""
	cfg, cfgErr := config.Load(resolveConfigPath(dir))
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "warning: config load failed (%v) — workspace price_table NOT applied to this listing\n", cfgErr)
	} else {
		tablePath = cfg.Compiler.PriceTable
	}
	registry, err := llm.LoadRegistry(tablePath)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	entries := registry.Entries()

	if outputFormat == "json" {
		type modelEntry struct {
			Key        string `json:"key"`
			Source     string `json:"source"`
			AsOf       string `json:"as_of,omitempty"`
			Input      string `json:"input_per_mtok,omitempty"`
			Cached     string `json:"cached_input_per_mtok,omitempty"`
			CacheWrite string `json:"cache_write_input_per_mtok,omitempty"`
			Output     string `json:"output_per_mtok,omitempty"`
			BatchIn    string `json:"batch_input_per_mtok,omitempty"`
			BatchOut   string `json:"batch_output_per_mtok,omitempty"`
		}
		out := make([]modelEntry, 0, len(entries))
		for _, e := range entries {
			me := modelEntry{Key: e.Key, Source: e.Price.Source}
			if !e.Price.AsOf.IsZero() {
				me.AsOf = e.Price.AsOf.Format("2006-01-02")
			}
			if e.Price.InputPerMTok != nil {
				me.Input = e.Price.InputPerMTok.String()
			}
			if e.Price.CachedInputPerMTok != nil {
				me.Cached = e.Price.CachedInputPerMTok.String()
			}
			if e.Price.OutputPerMTok != nil {
				me.Output = e.Price.OutputPerMTok.String()
			}
			if e.Price.CacheWritePerMTok != nil {
				me.CacheWrite = e.Price.CacheWritePerMTok.String()
			}
			if e.Price.BatchInputPerMTok != nil {
				me.BatchIn = e.Price.BatchInputPerMTok.String()
			}
			if e.Price.BatchOutputPerMTok != nil {
				me.BatchOut = e.Price.BatchOutputPerMTok.String()
			}
			out = append(out, me)
		}
		fmt.Println(cli.FormatJSON(true, map[string]any{"models": out}, ""))
		return nil
	}

	fmt.Fprint(cmd.OutOrStdout(), formatModelsText(entries))
	fmt.Fprintln(os.Stderr, "Note: builtin prices are estimates as of their as_of dates — verify and override via ~/.sage-wiki/prices.json or compiler.price_table.")
	return nil
}

// formatModelsText renders the effective registry for `cost models` —
// the audit view of exactly which price (and source) produced a number.
func formatModelsText(entries []llm.RegistryEntry) string {
	var b strings.Builder
	b.WriteString("Effective price registry (per 1M tokens):\n")
	for _, e := range entries {
		asOf := "unknown-date"
		if !e.Price.AsOf.IsZero() {
			asOf = e.Price.AsOf.Format("2006-01-02")
		}
		fmt.Fprintf(&b, "  %-42s in=%-8s cached=%-8s cache_write=%-8s out=%-8s batch_in=%-8s batch_out=%-8s as_of=%s source=%s\n",
			e.Key, decOrDash(e.Price.InputPerMTok), decOrDash(e.Price.CachedInputPerMTok),
			decOrDash(e.Price.CacheWritePerMTok), decOrDash(e.Price.OutputPerMTok),
			decOrDash(e.Price.BatchInputPerMTok), decOrDash(e.Price.BatchOutputPerMTok),
			asOf, e.Price.Source)
	}
	return b.String()
}

func decOrDash(d *decimal.Decimal) string {
	if d == nil {
		return "-"
	}
	return d.String()
}
