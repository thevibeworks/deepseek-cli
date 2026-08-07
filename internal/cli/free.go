package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

func newFreeCmd(o *Options) *cobra.Command {
	var url string

	cmd := &cobra.Command{
		Use:   "free",
		Short: "Use the API without a key",
		Long: strings.TrimSpace(`
Get a working DeepSeek API without signing up for one.

A gateway run by this project holds a real API key and relays requests to
DeepSeek on your behalf, metered and capped. Enrolling costs about a
second of CPU — a proof-of-work puzzle, which is what stands in for an
account here — and nothing else. No email, no card, no dashboard.

  deepseek free            enrol this machine
  deepseek free status     what is left of today
  deepseek free off        forget the enrolment

Once enrolled every command works as normal: chat, anthropic, respond,
fim, models. An API key, if you ever set one, always takes precedence —
this is a fallback for not having one, not a way around having one.

What you are agreeing to: your prompts travel to the gateway and on to
DeepSeek. The gateway records token counts and cost. It does not record
prompts or completions.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFreeEnrol(cmd.Context(), o, url)
		},
	}
	cmd.PersistentFlags().StringVar(&url, "url", "",
		"gateway to enrol with (default $DEEPSEEK_FREE_URL, then "+deepseek.DefaultGatewayURL+")")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "What is left of today's free quota",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runFreeStatus(cmd.Context(), o)
			},
		},
		&cobra.Command{
			Use:     "off",
			Aliases: []string{"forget", "logout"},
			Short:   "Forget the free-tier enrolment on this machine",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runFreeOff(o)
			},
		},
	)
	return cmd
}

// freeResult is the JSON shape of `deepseek free` and `free status`.
type freeResult struct {
	Enrolled bool                `json:"enrolled"`
	Gateway  string              `json:"gateway"`
	Subject  string              `json:"subject,omitempty"`
	Tier     string              `json:"tier,omitempty"`
	Quota    *deepseek.FreeQuota `json:"quota,omitempty"`
	Info     *deepseek.FreeInfo  `json:"info,omitempty"`
	// KeyInUse marks the case where an API key overrides all of this.
	KeyInUse bool `json:"api_key_in_use"`
}

func runFreeEnrol(ctx context.Context, o *Options, url string) error {
	if url == "" {
		url = deepseek.GatewayURL()
	}
	gw := deepseek.NewFreeGateway(url, o.Timeout)

	// Already enrolled: this is the same question `free status` answers,
	// and re-minting would burn a mint allowance for nothing.
	if existing, ok := deepseek.LoadFree(); ok && existing.BaseURL == gw.BaseURL {
		fmt.Fprintln(o.stderr, o.dim("already enrolled; showing status"))
		return runFreeStatus(ctx, o)
	}

	info, err := gw.Info(ctx)
	if err != nil {
		return fmt.Errorf("could not reach the free tier at %s: %w", gw.BaseURL, err)
	}
	o.printFreeOffer(gw.BaseURL, info)
	if info.Exhausted {
		return fmt.Errorf("the free tier has run out of credit for now — bring your own key: https://platform.deepseek.com/api_keys")
	}

	// The puzzle is the enrolment. Saying so, and showing it move, is
	// what keeps a second of silence from looking like a hang.
	fmt.Fprintf(o.stderr, "\nMinting an anonymous token (%d bits of proof-of-work)…\n", info.PoWBits)
	free, err := gw.Enrol(ctx, func(p deepseek.EnrolProgress) {
		if p.Done {
			fmt.Fprintf(o.stderr, "\r%s\n", o.dim(fmt.Sprintf(
				"  solved %d bits in %s (%s hashes)",
				p.Difficulty, p.Elapsed.Round(100*time.Millisecond), humanTokens(int(p.Hashes)))))
			return
		}
		fmt.Fprintf(o.stderr, "\r%s", o.dim(fmt.Sprintf("  %s hashes, %s…",
			humanTokens(int(p.Hashes)), p.Elapsed.Round(100*time.Millisecond))))
	})
	if err != nil {
		return err
	}
	if err := free.Save(); err != nil {
		return fmt.Errorf("enrolled, but could not save the token: %w", err)
	}

	quota, _ := gw.Quota(ctx, free.Token)
	if o.JSON {
		return o.emitValue(freeResult{
			Enrolled: true, Gateway: free.BaseURL, Subject: free.Subject,
			Tier: free.Tier, Quota: quota, Info: info,
		}, "")
	}

	fmt.Fprintf(o.stderr, "\nEnrolled. Saved to %s\n", deepseek.FreeFile())
	fmt.Fprintln(o.stderr, o.dim("  subject "+free.Subject))
	fmt.Fprintln(o.stderr, "\nTry it:")
	fmt.Fprintln(o.stderr, o.dim("  "+invokedAs()+` chat "explain the CAP theorem in one paragraph"`))
	return nil
}

func (o *Options) printFreeOffer(gateway string, info *deepseek.FreeInfo) {
	w := o.stderr
	fmt.Fprintln(w, "The free tier relays your prompts to DeepSeek through a gateway run by")
	fmt.Fprintln(w, "this project. No account, no API key.")
	fmt.Fprintln(w)
	row := func(k, v string) { fmt.Fprintf(w, "  %-9s %s\n", k, v) }
	row("gateway", gateway)
	row("model", info.Model)
	perDay := fmt.Sprintf("%s requests · %s input · %s output tokens",
		humanTokens(info.Limits.Requests), humanTokens(info.Limits.InputTokens), humanTokens(info.Limits.OutputTokens))
	// A gateway that does not offer web search sends no ration, and
	// printing "0 searches" would read as "you have used them all".
	if info.Limits.Searches > 0 {
		perDay += fmt.Sprintf(" · %d web searches", info.Limits.Searches)
	}
	row("per day", perDay)
	if info.MaxTokens > 0 {
		row("per call", fmt.Sprintf("%s output tokens max", humanTokens(info.MaxTokens)))
	}
	row("privacy", info.Privacy)
}

func runFreeStatus(ctx context.Context, o *Options) error {
	free, ok := deepseek.LoadFree()
	if !ok {
		if o.JSON {
			return o.emitValue(freeResult{Gateway: deepseek.GatewayURL()}, "")
		}
		fmt.Fprintln(o.stderr, "Not enrolled. Run: "+invokedAs()+" free")
		return nil
	}

	// A key set anywhere wins over the free tier, and someone reading
	// this page should not have to guess which one their next command
	// will use.
	keyInUse := false
	if _, err := deepseek.ResolveKey(o.APIKey); err == nil {
		keyInUse = true
	}

	gw := deepseek.NewFreeGateway(free.BaseURL, o.Timeout)
	quota, err := gw.Quota(ctx, free.Token)
	if err != nil {
		return err
	}

	if o.JSON {
		return o.emitValue(freeResult{
			Enrolled: true, Gateway: free.BaseURL, Subject: free.Subject,
			Tier: free.Tier, Quota: quota, KeyInUse: keyInUse,
		}, "")
	}

	w := o.stdout
	row := func(k, v string) { fmt.Fprintf(w, "  %-9s %s\n", k, v) }
	fmt.Fprintln(w, "free tier")
	row("gateway", free.BaseURL)
	row("subject", free.Subject)
	row("enrolled", free.Enrolled.Local().Format("2006-01-02"))
	today := fmt.Sprintf("%d/%d requests · %s/%s in · %s/%s out",
		quota.Used.Requests, quota.Limits.Requests,
		humanTokens(quota.Used.InputTokens), humanTokens(quota.Limits.InputTokens),
		humanTokens(quota.Used.OutputTokens), humanTokens(quota.Limits.OutputTokens))
	if quota.Limits.Searches > 0 {
		today += fmt.Sprintf(" · %d/%d searches", quota.Used.Searches, quota.Limits.Searches)
	}
	row("today", today)
	row("spent", money(quota.Used.SpentUSD)+" "+o.dim("(on our credits, not yours)"))
	if d := time.Until(quota.ResetsAt); d > 0 {
		row("resets", fmt.Sprintf("in %s %s", roundDuration(d), o.dim("(00:00 UTC)")))
	}

	if quota.Exhausted {
		fmt.Fprintln(o.stderr, "\n"+o.dim("The free tier has run out of credit. Bring your own key:")+
			"\n  https://platform.deepseek.com/api_keys")
	}
	if keyInUse {
		fmt.Fprintln(o.stderr, "\n"+o.dim("An API key is set, so commands use that instead of the free tier."))
	}
	return nil
}

func runFreeOff(o *Options) error {
	if _, ok := deepseek.LoadFree(); !ok {
		fmt.Fprintln(o.stderr, "Not enrolled; nothing to forget.")
		return nil
	}
	if err := deepseek.ForgetFree(); err != nil {
		return err
	}
	fmt.Fprintln(o.stderr, "Forgotten. Removed "+deepseek.FreeFile())
	fmt.Fprintln(o.stderr, o.dim("The token itself is not revoked; it simply is not on this machine any more."))
	return nil
}

// useFreeModel points a command's default model at the one the free tier
// actually serves.
//
// Only `fim` defaults to pro, and through the free tier that default
// would refuse every request for a reason the user did not choose and
// cannot see. An explicit -m is left alone: someone who asked for pro
// gets the gateway's refusal, which is the honest answer.
func (o *Options) useFreeModel(cmd *cobra.Command, model *string) {
	if cmd.Flags().Changed("model") || *model == deepseek.ModelFlash {
		return
	}
	if o.usingFree() == nil {
		return
	}
	fmt.Fprintln(o.stderr, o.dim("free tier serves "+deepseek.ModelFlash+
		" only; using it instead of this command's default "+*model))
	*model = deepseek.ModelFlash
}

// roundDuration renders a wait the way someone would say it.
func roundDuration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
