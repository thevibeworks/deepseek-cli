package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newRawCmd(o *Options) *cobra.Command {
	var (
		data      string
		anthropic bool
	)

	cmd := &cobra.Command{
		Use:   "raw <path> [method]",
		Short: "Send an arbitrary authenticated request",
		Long: strings.TrimSpace(`
Send any request to any path with this CLI's auth, base URL, retries and
error reporting, and print the response verbatim.

This is the escape hatch. Every other command is a typed convenience over
exactly this call, so an endpoint or a parameter DeepSeek ships tomorrow
is reachable today without waiting for a release here.

The method defaults to GET, or to POST when a body is given.

  deepseek raw /models
  deepseek raw /chat/completions --data @request.json
  deepseek raw /anthropic/v1/messages --data @req.json --anthropic-auth`),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}

			body, err := readJSON(data)
			if err != nil {
				return fmt.Errorf("--data: %w", err)
			}

			method := "GET"
			if len(body) > 0 {
				method = "POST"
			}
			if len(args) > 1 {
				method = strings.ToUpper(args[1])
			}

			client, err := o.client()
			if err != nil {
				return err
			}
			raw, err := client.Raw(ctxOf(cmd), method, path, body, anthropic)
			if err != nil {
				return err
			}

			// Always JSON here: raw is for looking at the wire, so the
			// response is printed as-is rather than reduced to text.
			if o.JQ != "" {
				return pipeJQ(o.stdout, raw, o.JQ)
			}
			if !json.Valid(raw) {
				_, err := o.stdout.Write(append(raw, '\n'))
				return err
			}
			return writeJSON(o.stdout, raw)
		},
	}

	cmd.Flags().StringVarP(&data, "data", "d", "", "request body as JSON or @file")
	cmd.Flags().BoolVar(&anthropic, "anthropic-auth", false, "authenticate with x-api-key instead of a bearer token")
	return cmd
}
