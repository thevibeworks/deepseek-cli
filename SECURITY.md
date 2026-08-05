# Security

## Reporting

Report vulnerabilities through GitHub's private advisory form:
https://github.com/thevibeworks/deepseek-cli/security/advisories/new

Please do not open a public issue for a vulnerability.

## What this tool touches

- **Your API key.** Read from `--api-key`, `$DEEPSEEK_API_KEY`, or
  `~/.config/deepseek/api_key`. It is sent to the configured base URL and
  is never written to disk by this tool, never logged (`-v` logs request
  metadata; `-vv` logs bodies, which contain your prompts but not the
  Authorization header), and never included in the usage ledger.
- **Your prompts.** Sent to DeepSeek. Conversations saved with
  `--session` are written to `~/.local/state/deepseek/sessions/` at mode
  0600 and stay on your machine.
- **The usage ledger.** `~/.local/state/deepseek/usage.jsonl`, mode 0600,
  holds token counts and timings — no prompt text.

## If you set `--base-url`

Everything above, including your API key and prompts, goes to that host
instead. Only point it at a gateway you control.
