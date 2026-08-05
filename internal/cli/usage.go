package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/ledger"
)

func newUsageCmd(o *Options) *cobra.Command {
	var since string
	var raw bool

	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Report what this CLI has spent",
		Long: strings.TrimSpace(`
Summarise the local usage ledger: every call this CLI made, what it cost,
and how much the context cache saved.

The ledger is a plain JSONL file at ` + ledger.Path() + `, written by
every command unless --no-ledger was passed. It is local bookkeeping, not
billing: costs are estimated from the published USD rate card, so they
will not match an invoice to the cent. The token counts are exact, and
they are what gets stored — so old calls can be repriced when the rate
card changes.

  deepseek usage                 # today
  deepseek usage --since 7d
  deepseek usage --since all --json`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cutoff, err := ledger.ParseSince(since)
			if err != nil {
				return err
			}
			entries, err := ledger.Load(cutoff)
			if err != nil {
				return fmt.Errorf("reading the ledger: %w", err)
			}
			if raw {
				return o.emitValue(entries, formatEntries(entries))
			}
			report := ledger.Summarize(entries, cutoff)
			return o.emitValue(report, formatReport(report, since))
		},
	}

	cmd.Flags().StringVar(&since, "since", "today", "window: today, yesterday, 7d, 24h, all, or a date")
	cmd.Flags().BoolVar(&raw, "entries", false, "list the individual calls instead of the summary")
	return cmd
}

func formatReport(r *ledger.Report, since string) string {
	if r.Total.Calls == 0 {
		return fmt.Sprintf("no calls recorded for %s", since)
	}

	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\tCALLS\tIN\tCACHED\tOUT\tCOST")
	for _, model := range r.Models() {
		s := r.ByModel[model]
		fmt.Fprintf(w, "%s\t%d\t%s\t%.0f%%\t%s\t%s\n",
			model, s.Calls, humanTokens(s.InputTokens), s.CacheHitRate()*100, humanTokens(s.OutputTokens), money(s.CostUSD))
	}
	if len(r.ByModel) > 1 {
		t := r.Total
		fmt.Fprintf(w, "total\t%d\t%s\t%.0f%%\t%s\t%s\n",
			t.Calls, humanTokens(t.InputTokens), t.CacheHitRate()*100, humanTokens(t.OutputTokens), money(t.CostUSD))
	}
	w.Flush()

	if len(r.ByAPI) > 1 {
		var parts []string
		for _, api := range r.APIs() {
			parts = append(parts, fmt.Sprintf("%s %d", api, r.ByAPI[api].Calls))
		}
		fmt.Fprintf(&b, "\nby format: %s", strings.Join(parts, ", "))
	}
	if r.Total.SavedUSD > 0 {
		fmt.Fprintf(&b, "\ncontext cache saved ~%s (%s of %s prompt tokens replayed)",
			money(r.Total.SavedUSD), humanTokens(r.Total.CacheHitTokens), humanTokens(r.Total.InputTokens))
	}
	fmt.Fprint(&b, "\ncosts are estimates from the published USD rate card, not billed amounts")
	return b.String()
}

func formatEntries(entries []ledger.Entry) string {
	if len(entries) == 0 {
		return "no calls recorded"
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tFORMAT\tMODEL\tIN\tCACHED\tOUT\tCOST")
	for _, e := range entries {
		cached := 0.0
		if e.InputTokens > 0 {
			cached = float64(e.CacheHitTokens) / float64(e.InputTokens) * 100
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.0f%%\t%s\t%s\n",
			e.Time.Local().Format("15:04:05"), e.API, strings.TrimPrefix(e.Model, "deepseek-v4-"),
			humanTokens(e.InputTokens), cached, humanTokens(e.OutputTokens), money(e.CostUSD))
	}
	w.Flush()
	return strings.TrimRight(b.String(), "\n")
}
