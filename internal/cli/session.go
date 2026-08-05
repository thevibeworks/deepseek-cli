package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/session"
)

func newSessionCmd(o *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "session",
		Aliases: []string{"sessions"},
		Short:   "Inspect the conversations chat --continue replays",
		Long: strings.TrimSpace(`
The API is stateless — it stores nothing between calls — so multi-turn
conversations live on this machine. 'chat --continue' reads and writes the
session named "last"; 'chat --session work' keeps a separate thread.

Sessions are plain JSON, one file per conversation. Long ones cost real
input tokens on every turn, which is what 'ls' shows you.`),
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:     "ls",
			Aliases: []string{"list"},
			Short:   "List stored conversations",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				metas, err := session.List()
				if err != nil {
					return err
				}
				return o.emitValue(metas, formatSessions(metas))
			},
		},
		&cobra.Command{
			Use:   "show [name]",
			Short: "Print a conversation",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				s, err := session.Load(sessionArg(args))
				if err != nil {
					return err
				}
				return o.emitValue(s, formatTranscript(s))
			},
		},
		&cobra.Command{
			Use:   "rm [name]",
			Short: "Delete a conversation",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				name := sessionArg(args)
				if err := session.Remove(name); err != nil {
					return err
				}
				fmt.Fprintf(o.stderr, "removed session %s\n", name)
				return nil
			},
		},
	)
	return cmd
}

func sessionArg(args []string) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	return session.Default
}

func formatSessions(metas []session.Meta) string {
	if len(metas) == 0 {
		return "no stored conversations — start one with: deepseek chat -c \"...\""
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTURNS\tMODEL\tUPDATED")
	for _, m := range metas {
		model := m.Model
		if model == "" {
			model = "-"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", m.Name, m.Turns, strings.TrimPrefix(model, "deepseek-v4-"), ago(m.Updated))
	}
	w.Flush()
	return strings.TrimRight(b.String(), "\n")
}

func formatTranscript(s *session.Session) string {
	if s.Empty() {
		return fmt.Sprintf("session %q is empty", s.Name)
	}
	var b strings.Builder
	for i, m := range s.Messages {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "── %s ──\n%s\n", m.Role, strings.TrimRight(m.Content, "\n"))
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "   tool_call %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
