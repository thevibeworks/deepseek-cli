# AGENTS.md

> Agent instructions for [deepseek-cli](https://github.com/thevibeworks/deepseek-cli)
> — the whole DeepSeek API from the command line.

## What this tool does

`deepseek` calls the DeepSeek API: chat completions in three wire formats
(OpenAI, Anthropic Messages, OpenAI Responses), FIM completion, model
list, and account balance. It also keeps a local ledger of what each call
cost and can replay multi-turn conversations.

Use it when you need a model answer from a shell, when you need to check
whether a DeepSeek key works, or when you need to know what something
cost. Do not use it as an agent runtime — it prints tool calls, it does
not execute them.

## Prerequisites

```bash
go install github.com/thevibeworks/deepseek-cli/cmd/deepseek@latest
export DEEPSEEK_API_KEY=sk-...
```

Verify before a run of real work — one call, all six endpoints:

```bash
deepseek check --json
# {"base_url":"...","key_set":true,"probes":[...],"ok":true}
```

If `ok` is false, read `probes[].error`. Exit code 2 means the key is
missing or rejected; 3 means the balance is exhausted. Neither is worth
retrying.

## The output contract

- **stdout is data.** The answer text, or with `--json` the API's own
  response body, unwrapped and unmodified.
- **stderr is status.** Chain of thought, the usage line, truncation
  warnings, verbose HTTP, errors.
- Safe to pipe: `deepseek chat "..." > answer.txt` writes the answer and
  nothing else.

## Commands

### Chat (start here)

```bash
deepseek chat "why is the sky blue"
deepseek chat "explain" --file main.go --file main_test.go
git diff | deepseek chat "write a commit message"
deepseek chat "..." --json | jq -r '.choices[0].message.content'
```

Arguments are the instruction; piped stdin and `--file` are the material.
They compose — you can use all three at once.

Key flags:

| Flag | Effect |
| --- | --- |
| `-m, --model` | `deepseek-v4-flash` (default) or `deepseek-v4-pro` |
| `--think on\|off` | thinking mode; default is the API's own, which is on |
| `-e, --effort low\|high\|max` | reasoning effort |
| `--max-tokens N` | cap generated tokens |
| `--response-format json_object` | guarantee valid JSON output |
| `--tool @file.json` | declare a tool (repeatable) |
| `-s, --system` | system prompt, inline or `@file` |
| `-c, --continue` | continue the conversation named `last` |
| `--session NAME` | read and write a named conversation |
| `--stream=false` | wait for the whole answer |

### The other formats

```bash
deepseek anthropic "..."   # Anthropic Messages — what Claude Code speaks
deepseek respond "..."     # OpenAI Responses — what Codex speaks
deepseek fim "def f():" --suffix "    return x"
```

Reach for `anthropic` or `respond` when the task is specifically about
those wire formats. For a plain answer, use `chat`.

Only `respond` has JSON Schema output and server-side web search:

```bash
deepseek respond "Berlin" -s "Return city and country." --schema @city.json
deepseek respond "what shipped in Go 1.26" --web-search
```

### Account and cost

```bash
deepseek models --json
deepseek balance --json          # exits 3 if exhausted
deepseek usage --since 7d --json # local ledger, not billing
deepseek usage --entries --json  # individual calls
```

### Escape hatch

```bash
deepseek raw /models
deepseek raw /chat/completions --data @request.json
```

Anything the typed commands do not cover. Same auth, retries and error
reporting.

## JSON response shapes

`--json` returns the API's response verbatim, so the shapes are the
documented DeepSeek ones:

```
chat --json       {"id","object","created","model","choices":[{"message":{"content","reasoning_content","tool_calls"},"finish_reason"}],"usage":{...}}
anthropic --json  {"id","type","role","model","content":[{"type","text"}],"stop_reason","usage":{...}}
respond --json    {"id","object","status","model","output":[{"type","content":[{"type","text"}]}],"usage":{...}}
fim --json        {"id","object","model","choices":[{"text","finish_reason"}],"usage":{...}}
models --json     {"object":"list","data":[{"id","object","owned_by"}]}
balance --json    {"is_available","balance_infos":[{"currency","total_balance",...}]}
```

These are computed locally, not from the API:

```
usage --json      {"since","total":{...},"by_model":{...},"by_api":{...}}
check --json      {"base_url","key_set","ok","probes":[{"name","path","ok","detail","error","ms"}]}
session ls --json [{"name","model","turns","updated","bytes"}]
```

Note the usage field names differ per format, exactly as the API sends
them: chat has `prompt_cache_hit_tokens`, Anthropic has
`cache_read_input_tokens` (and its `input_tokens` **excludes** cache
reads), Responses has `input_tokens_details.cached_tokens`.

## Exit codes

| Code | Meaning | What to do |
| --- | --- | --- |
| 0 | success | — |
| 1 | error | read stderr |
| 2 | auth | the key is missing or rejected; do not retry |
| 3 | no balance | top up; do not retry |
| 4 | rate limited | back off, then retry |
| 130 | interrupted | — |

Transport failures and 429/5xx are already retried internally with
backoff, so a non-zero exit means retrying in your own loop probably will
not help.

## Multi-turn

The API stores nothing. Conversations live in
`~/.local/state/deepseek/sessions/`.

```bash
deepseek chat "read this" --file spec.md --session review
deepseek chat "now list the risks"        --session review
deepseek session show review --json
deepseek session rm review
```

One rule matters and the tool handles it: when a request carries `--tool`,
DeepSeek requires every assistant message's `reasoning_content` to be
replayed on every later request or it answers 400. Sessions replay it
automatically once tools have been used, and strip it when they have not
(where the API ignores it, so sending it only costs tokens).

## Cost

Every call prints a usage line to stderr and appends to
`~/.local/state/deepseek/usage.jsonl`.

- `--no-stats` hides the line; `--no-ledger` skips the write. Use both
  for a quiet run.
- Costs are estimates from the published USD rate card, not billed
  amounts. Token counts are exact.
- Cached input tokens cost 50× less than uncached ones. Put the stable
  part of a prompt first — the same system prompt and files across calls
  — and `deepseek usage` will show the saving.

## Cautions

- **Text only.** Images, documents and search-result blocks are rejected
  by the API in every format.
- **Thinking is on by default** and adds a flat 79 input tokens per
  request before generating anything. Use `--think off` for short
  factual work.
- **`respond` is flash-only.** `--model deepseek-v4-pro` will fail there.
- **`fim` caps output at 4K tokens** and never thinks.
- **Slow starts are normal**, up to ten minutes before inference begins
  under load. Do not set an aggressive `--timeout` and call it a failure.
- **Tool calls are printed, not executed.**
