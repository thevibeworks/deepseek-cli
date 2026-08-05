package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

func newModelsCmd(o *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List the models this key can reach (GET /models)",
		Long: strings.TrimSpace(`
List the available models. The text view joins the API's answer with the
published rate card, so the price you are about to pay is on screen next
to the model you are about to pick. --json returns the API's list alone.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := o.client()
			if err != nil {
				return err
			}
			list, raw, err := client.Models(ctxOf(cmd))
			if err != nil {
				return err
			}

			var b strings.Builder
			w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "MODEL\tOWNER\tIN (CACHED)\tIN (MISS)\tOUT")
			for _, m := range list.Data {
				price, ok := deepseek.PriceFor(m.ID)
				if !ok {
					fmt.Fprintf(w, "%s\t%s\t-\t-\t-\n", m.ID, m.OwnedBy)
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t$%g\t$%g\t$%g\n",
					m.ID, m.OwnedBy, price.CacheHitInput, price.CacheMissInput, price.Output)
			}
			w.Flush()
			fmt.Fprint(&b, "\nUSD per 1M tokens, from the published rate card. 1M context, 384K max output.")

			return o.emit(raw, b.String())
		},
	}
}

func newBalanceCmd(o *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "balance",
		Short: "Show the account balance (GET /user/balance)",
		Long: strings.TrimSpace(`
Show what is left in the account. An account can hold more than one
currency at once, so this lists every one the API reports.

Exits 3 when the balance is exhausted, which is the same code a 402 from
any other command produces — so a script can check once up front.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := o.client()
			if err != nil {
				return err
			}
			bal, raw, err := client.Balance(ctxOf(cmd))
			if err != nil {
				return err
			}

			var b strings.Builder
			funded := bal.Funded()
			if len(funded) == 0 {
				// Report every row when none is funded: "0.00 USD, 0.00 CNY"
				// is more useful than an empty table.
				funded = bal.BalanceInfos
			}
			w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENCY\tTOTAL\tTOPPED UP\tGRANTED")
			for _, info := range funded {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", info.Currency, info.TotalBalance, info.ToppedUpBalance, info.GrantedBalance)
			}
			w.Flush()
			if !bal.IsAvailable {
				fmt.Fprint(&b, "\nnot available for API calls — top up at https://platform.deepseek.com/top_up")
			}

			if err := o.emit(raw, b.String()); err != nil {
				return err
			}
			if !bal.IsAvailable {
				return &exitError{code: deepseek.ExitBalance, msg: "balance exhausted"}
			}
			return nil
		},
	}
}

// exitError carries a specific exit code for a command that failed in a
// way worth distinguishing, without an HTTP error behind it.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }
func (e *exitError) ExitCode() int { return e.code }

func ctxOf(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
