---
name: deepseek
description: Call the DeepSeek API from the shell — chat completions in OpenAI, Anthropic Messages or OpenAI Responses format, FIM completion, model list, account balance, and local cost accounting. Use when the task needs a DeepSeek model answer, needs to verify a DeepSeek API key or endpoint, needs structured or JSON output from a model, or asks what DeepSeek usage has cost. Triggers on deepseek, deepseek-v4, flash, pro, "ask the model", "check my API key", token cost, context cache, "deepseek without an API key", free tier.
---

# deepseek

Call the DeepSeek API from a shell. One binary, six endpoints.

## Check first

```bash
deepseek check --json
```

`ok: true` means the key works and all six endpoints answered. Exit 2 is
a bad or missing key, exit 3 is an exhausted balance — neither is worth
retrying, and both need the human.

If the binary is missing:
`go install github.com/thevibeworks/deepseek-cli/cmd/deepseek@latest`

If there is no API key, `deepseek free` enrols against a metered gateway
run by the project — about a second of CPU, no account. Use it to get one
call working; tell the human it is capped, that their prompts transit a
third party, and that a real key removes both facts.

## Ask a model

```bash
deepseek chat "why is the sky blue"
deepseek chat "explain this" --file main.go
git diff | deepseek chat "write a commit message"
```

Arguments are the instruction; `--file` and piped stdin are the material.
They compose.

For a parseable answer:

```bash
deepseek chat "..." --json | jq -r '.choices[0].message.content'
deepseek chat "list 3 primes as JSON with key primes" --response-format json_object
```

stdout is the answer, stderr is status. Redirecting stdout gets the
answer alone.

## Pick the model and the effort

| Want | Use |
| --- | --- |
| Cheap, fast, most work | default (`deepseek-v4-flash`) |
| Hardest reasoning | `--model deepseek-v4-pro --effort max` |
| Short factual answer | `--think off` — no reasoning, no template |
| Cheap reasoning | `--effort low` — still reasons, but adds **no** input template on flash |
| Long answer | `--max-tokens N` |

Thinking is on by default. The template it adds to the input depends on
the effort, measured live rather than documented:

| `--effort` | flash | pro |
| --- | --- | --- |
| `none` | +0, thinking off | +0, thinking off |
| `minimal`, `low` | +0 | +0 |
| `medium`, `high`, `xhigh` | +79 | +0 |
| `max` | +92 | +79 |

So `--effort low` on flash is the cheap way to keep reasoning, and
`--think off` (or `--effort none`) is the cheap way to drop it.

## Multi-turn

The API stores nothing; conversations live on this machine.

```bash
deepseek chat "read this spec" --file spec.md --session review
deepseek chat "now list the risks"           --session review
deepseek session show review
```

`--continue` is shorthand for the session named `last`. The tool handles
DeepSeek's reasoning-replay rule itself — do not try to manage
`reasoning_content` by hand.

## Other wire formats

Only reach for these when the task is about the format itself.

```bash
deepseek anthropic "..."    # the format Claude Code and Anthropic SDKs speak
deepseek respond "..."      # the format Codex speaks
deepseek fim "def f():" --suffix "    return x"
```

Two things exist only on `respond`:

```bash
deepseek respond "Berlin" -s "Return city and country." --schema @city.json
deepseek respond "what shipped recently in Go" --web-search
```

## Tools

```bash
deepseek chat "weather in Hangzhou?" --tool @weather.json
```

Prints the tool calls the model wants; does **not** run them. One tool
file works across all formats — both the OpenAI `parameters` and the
Anthropic `input_schema` spellings are accepted.

## Cost

```bash
deepseek balance          # what is left, per currency
deepseek usage --since 7d # what this CLI has spent
```

Cached input tokens cost about 30× less than uncached ones, so put the
stable part of a prompt first — same system prompt, same files, in the
same order across calls. `deepseek usage` reports what the cache saved.

Since 2026-08-16 the rate card is time-of-day: peak hours 01:00–04:00
and 06:00–10:00 UTC bill at twice the off-peak rate. `deepseek pricing`
names the period in force right now, offline. Batch work that can wait
should run off-peak; never quote a price without naming the period.

Costs shown are estimates from the published rate card, not billed
amounts. Say so when reporting them.

Add `--no-stats --no-ledger` when the usage line would pollute output
being captured.

## Answer DeepSeek API questions from the docs, not from memory

The binary carries every page of api-docs.deepseek.com plus the FAQ. Use
it instead of recalling how the API works — it changes monthly.

```bash
deepseek docs search "context cache"          # free, offline
deepseek docs show guides/thinking_mode       # free, offline
deepseek docs changelog                       # free, offline
deepseek docs ask "when must I replay reasoning_content?"   # costs a request
```

`ask` sends the relevant pages and requires the answer to cite them, so
every claim traces to a page path and a URL. Each command prints how old
the snapshot is: if it is more than a month old, say so in your answer or
run `deepseek docs sync`.

## Count tokens before sending something large

```bash
deepseek tokens --file big.md --json
deepseek tokens --offline --file big.md    # free estimate, upper bound
```

Exact counts come from the API and are billed as input — the text is
really sent. Say "measured" for those and "estimated" for `--offline`.

## Anything not covered

```bash
deepseek raw /models
deepseek raw /chat/completions --data @request.json
```

## Is it up

```bash
deepseek status --json    # free: two calls that generate no tokens
```

Answers whether the API is reachable with this key from this machine.
That is not the same as DeepSeek's incident page, which the output links.

## Without an API key

```bash
deepseek free                # enrol; prints what leaves the machine first
deepseek free status --json  # what is left of today
```

Then every command works as normal. Limits per UTC day: 30 requests, 60K
input, 20K output tokens, flash only. A pro request is refused, not
downgraded — if the task needs pro, it needs a key.

Errors carrying `"type":"free_tier_*"` came from the gateway, not from
DeepSeek. Their message already contains the next step; pass it through
rather than adding DeepSeek's advice, which assumes an account the user
does not have. A `free_tier_exhausted` 402 will not recover today.

## Rules

- Report costs as estimates, never as billed amounts.
- Do not retry on exit 2 or 3 — bring them to the human.
- Transport errors and 429/5xx are already retried internally; a non-zero
  exit means your own retry loop probably will not help either.
- The API is text-only: no images, no documents.
- Slow starts up to ten minutes are normal under load, not a failure.
- Never suggest the free tier to someone who already has a key: a key is
  unmetered, private, and always takes precedence anyway.
