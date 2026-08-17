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

With no key, `deepseek free` enrols against a gateway run by this project
and everything below works with a daily quota. Use it when you have no
key and need one call to work; do not use it for volume, and never
suggest it to someone who already has a key — a real key always takes
precedence and is unmetered.

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
deepseek pricing --json          # the schedule and the billing period right now
```

Pricing has been time-of-day since 2026-08-16 16:00 UTC: peak hours
01:00–04:00 and 06:00–10:00 UTC bill at twice the off-peak rate. Never
quote a DeepSeek price without saying which period it is — `pricing`
computes the current one locally, no network, nothing spent, from the
same schedule the cost estimates use.

### Documentation, offline

The binary carries every page of api-docs.deepseek.com plus the FAQ.
Use it before answering a DeepSeek API question from memory — the API
changes monthly and this is the vendor's own text, with a source URL per
page.

```bash
deepseek docs search "context cache" --json   # [{"path","title","source","score","snippet"}]
deepseek docs show guides/thinking_mode       # markdown to stdout, URL to stderr
deepseek docs ask "..." --json                # a grounded answer, with citations
deepseek docs changelog --json                # releases, newest first
deepseek docs sync                            # refresh the snapshot
```

`ask` costs a request; `search`, `show` and `changelog` cost nothing and
work with no network. Every one of them prints the corpus age on stderr;
treat a corpus more than a month old as suspect and say so, or sync.

### Counting tokens

```bash
deepseek tokens --file big.md --json
# {"method":"api","model":"...","total":{"tokens","chars","bytes"},"items":[...],
#  "chat":{"input","input_thinking"}}
```

Exact, via the FIM endpoint's `prompt_tokens` minus its BOS token. It is
a billed request that sends the text — use `--offline` for a free local
estimate, which over-counts English and is labelled an upper bound.

### Is it up

```bash
deepseek status --json
# {"base_url","ok","models":[...],"latency_ms","balance","status_page"}
```

Two calls that generate no tokens, so it is free and safe in a loop.
Exits 2 on a bad key, 0 when reachable. `deepseek check` is the fuller
preflight and does cost a fraction of a cent.

### No key

```bash
deepseek free                # enrol: ~1s of CPU, no account, no card
deepseek free status --json  # {"enrolled","gateway","subject","quota":{...}}
deepseek free off            # forget the enrolment on this machine
```

Free-tier limits, per UTC day: 30 requests, 60K input tokens, 20K output
tokens, 3 web searches, 4K output per call, 128KB per request body,
`deepseek-v4-flash` only. A request for pro is **refused, not
downgraded**. `models` and `status` cost no quota; everything that can
generate a token does.

`respond --web-search` works on the free tier and spends one of the three
daily searches. It is rationed that tightly because DeepSeek reads whole
pages into the prompt — one measured search request billed 40K input
tokens, about ten ordinary turns — so treat it as a few lookups a day, not
a research loop. Other server-side tools are still refused.

Errors from the gateway carry `"type":"free_tier_*"` and a message that
already contains the next step — do not append DeepSeek's own advice to
them, because a free-tier user has no account to top up.

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
pricing --json    {"now_utc","now_local","now_beijing","period","multiplier","next_change","reprice_at","peak_windows_utc":[...],"peak_multiplier","current":{...},"off_peak":{...},"peak":{...},"source"}
check --json      {"base_url","key_set","ok","probes":[{"name","path","ok","detail","error","ms"}]}
session ls --json [{"name","model","turns","updated","bytes"}]
status --json     {"base_url","ok","models":[...],"latency_ms","balance","status_page"}
tokens --json     {"method","model","total":{...},"items":[...],"chat":{...}}
docs search --json [{"path","title","source","score","snippet"}]
free status --json {"enrolled","gateway","subject","tier","quota":{"used","limits","resets_at"},"api_key_in_use"}
```

The ledger records `tokens` and `docs` calls under those API names, so
`usage --json` can separate measurement and documentation spend from
work.

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
- Cached input tokens cost about 30× less than uncached ones. Put the
  stable part of a prompt first — the same system prompt and files across
  calls — and `deepseek usage` will show the saving. Prompt structure is
  still the biggest lever on a bill; the hour of the day comes second,
  and it is worth at most 2×.

## Cautions

- **Text only.** Images, documents and search-result blocks are rejected
  by the API in every format.
- **The thinking surcharge depends on effort**, and not as documented.
  The template added to your input is fixed per level, constant across
  prompt length, measured live on 2026-08-05:

  | `--effort` | flash | pro |
  | --- | --- | --- |
  | `none` | +0, thinking off | +0, thinking off |
  | `minimal`, `low` | +0 | +0 |
  | `medium`, `high`, `xhigh` | +79 | +0 |
  | `max` | +92 | +79 |

  So on flash, `--effort low` removes the whole surcharge and still
  reasons. `--think off` removes the reasoning as well. `none` and
  `minimal` are accepted by the API and documented nowhere.
- **`respond` takes both models** since V4-Pro's official release
  (2026-08-12). Older notes saying it is flash-only are stale.
- **`fim` caps output at 4K tokens** and never thinks.
- **Slow starts are normal**, up to ten minutes before inference begins
  under load. Do not set an aggressive `--timeout` and call it a failure.
- **Tool calls are printed, not executed.**
- **A strict tool moves the request to the beta path** automatically.
  Sent to the stable path, `strict` is ignored and the schema guarantee
  silently does not hold.
- **`--user-id` is validated locally** against the API's rule
  (`[a-zA-Z0-9_-]{1,512}`). It is not a place for personal data.
- **On the free tier, prompts transit a third party.** They go to this
  project's gateway and on to DeepSeek. Token counts and cost are
  recorded; prompts and completions are not. Say so before suggesting it
  for anything sensitive.
- **The free tier can run out mid-task.** A 402 means the shared credit
  pool is empty and will not recover today; a 429 with `free_tier_quota`
  resets at 00:00 UTC. Neither is worth retrying — switch to a key.

## Working on this repo

Everything above is about using the tool. If you are changing it:

- `make check` before proposing anything. It builds, vets, runs the Go
  tests, and checks the rate card, the docs corpus, the gateway and the
  site.
- **Touching `site/`?** Read `DESIGN.md` first — it is the material
  contract: the tokens, the two gestures the page is built on, and the
  responsive rules. `TASTE.md` holds prior rulings, each with the reason
  it was rejected; read it before re-proposing something it already
  refused. Run `site/bans.sh` before you ship (`make site-check` does).
- The site's HTML is generated. Edit `site/build.py`, not the committed
  `index.html` files, and rerun `python3 site/build.py`.
- Visual changes get looked at in a browser at more than one width before
  they are called done. The stylesheet has three breakpoints and a
  document that must never scroll sideways.
