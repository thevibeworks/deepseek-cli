package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// StatusPage is where DeepSeek publishes incidents. This CLI links it
// rather than scraping it: the page is a client-rendered app with no
// documented JSON endpoint, and a parser written against markup nobody
// promised to keep would eventually report "all systems operational"
// because a div was renamed. A wrong all-clear is worse than a link.
const StatusPage = "https://status.deepseek.com/"

// statusResult is the JSON shape of `deepseek status`.
type statusResult struct {
	BaseURL string   `json:"base_url"`
	OK      bool     `json:"ok"`
	Models  []string `json:"models,omitempty"`
	Latency int64    `json:"latency_ms"`
	Balance string   `json:"balance,omitempty"`
	Error   string   `json:"error,omitempty"`
	// StatusPage is carried in the JSON too, so a script that finds this
	// down has somewhere to send a human.
	StatusPage string `json:"status_page"`
}

func newStatusCmd(o *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Is the API up, for this key, from here",
		Long: strings.TrimSpace(`
Answer the question you actually have when something stops working: is
the DeepSeek API reachable right now, with this key, from this machine.

Two calls, GET /models and GET /user/balance. Neither generates tokens,
so this costs nothing and can be run in a loop. It is not the same
question as DeepSeek's status page, which reports incidents affecting
everyone — a working API and a broken proxy in front of you look
identical there and different here.

For the full endpoint-by-endpoint preflight, including the completion
endpoints, use ` + "`deepseek check`" + ` instead; that one spends a
fraction of a cent.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, o)
		},
	}
}

func runStatus(cmd *cobra.Command, o *Options) error {
	client, err := o.client()
	if err != nil {
		return err
	}
	res := &statusResult{
		BaseURL:    client.BaseURL,
		StatusPage: StatusPage,
	}

	start := time.Now()
	list, _, err := client.Models(ctxOf(cmd))
	res.Latency = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		if emitErr := o.emitValue(res, formatStatus(res)); emitErr != nil {
			return emitErr
		}
		return err
	}
	res.OK = true
	for _, m := range list.Data {
		res.Models = append(res.Models, m.ID)
	}

	// Balance is a second question — "up" and "usable" differ when the
	// account is empty — but a failure here is not a failure of the API,
	// so it never flips the verdict.
	if bal, _, err := client.Balance(ctxOf(cmd)); err == nil {
		if funded := bal.Funded(); len(funded) > 0 {
			res.Balance = funded[0].TotalBalance + " " + funded[0].Currency
		} else if !bal.IsAvailable {
			res.Balance = "exhausted"
		}
	} else {
		o.verbosef("balance unavailable: %v", err)
	}

	return o.emitValue(res, formatStatus(res))
}

func formatStatus(res *statusResult) string {
	var b strings.Builder
	if !res.OK {
		fmt.Fprintf(&b, "unreachable  %s  %dms\n  %s\n", res.BaseURL, res.Latency, firstLine(res.Error))
		fmt.Fprintf(&b, "\nincidents: %s\nfull preflight: deepseek check", res.StatusPage)
		return b.String()
	}
	fmt.Fprintf(&b, "ok  %s  %dms", res.BaseURL, res.Latency)
	if len(res.Models) > 0 {
		fmt.Fprintf(&b, "  %s", strings.Join(shortModels(res.Models), ", "))
	}
	if res.Balance != "" {
		fmt.Fprintf(&b, "  %s", res.Balance)
	}
	fmt.Fprintf(&b, "\n\nreachable with this key, from here. DeepSeek's own incident page,\nwhich answers the different question of whether it is down for everyone:\n%s", res.StatusPage)
	return b.String()
}

// shortModels trims the vendor prefix for a status line that has to fit
// on one row next to a latency and a balance.
func shortModels(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strings.TrimPrefix(id, "deepseek-v4-")
	}
	return out
}
