<p align="center">
  <h1 align="center">deepseek</h1>
  <p align="center">
    The whole DeepSeek API, from the terminal.<br>
    Four wire formats, one binary. Multi-turn that survives the reasoning
    round-trip. And it tells you what every call cost.
  </p>
</p>

<p align="center">
  <a href="https://github.com/thevibeworks/deepseek-cli/releases"><img src="https://img.shields.io/github/v/release/thevibeworks/deepseek-cli?color=blue&label=release" alt="Release"></a>
  <a href="https://github.com/thevibeworks/deepseek-cli/actions/workflows/ci.yml"><img src="https://github.com/thevibeworks/deepseek-cli/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="#commands"><img src="https://img.shields.io/badge/API%20coverage-6%2F6%20endpoints-brightgreen" alt="API coverage: 6 of 6 endpoints"></a>
  <a href="#development"><img src="https://img.shields.io/badge/tests-102%20%C2%B7%2068%25%20covered-brightgreen" alt="102 tests, 68% statement coverage"></a>
  <br>
  <a href="https://api-docs.deepseek.com"><img src="https://img.shields.io/badge/DeepSeek%20API%20docs-2026--08--02-8a2be2" alt="Implemented against the DeepSeek API docs of 2026-08-02"></a>
  <a href="https://api-docs.deepseek.com/quick_start/pricing"><img src="https://img.shields.io/badge/models-v4--flash%20%7C%20v4--pro-0ea5e9" alt="Models: deepseek-v4-flash and deepseek-v4-pro"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="MIT License"></a>
</p>

<p align="center">
  <sub>
    Badges that could rot are held down by tests:
    <a href="internal/cli/e2e_test.go"><code>TestCheckCoversEveryEndpoint</code></a> fails if an
    endpoint is added without <code>deepseek check</code> probing it, and
    <code>make cover-gate</code> fails CI if coverage drops below its floor.
  </sub>
</p>

<div align="center">

[Install](#install) · [Quick start](#quick-start) · [Commands](#commands) · [Cost](#what-things-cost) · [For agents](#for-agents) · [Config](#configuration)

</div>

---

> Unofficial tool. Not affiliated with DeepSeek.

DeepSeek serves the same two models through four different wire formats —
OpenAI chat, OpenAI Responses, Anthropic Messages, and FIM. Every other
client picks one. This speaks all four, so you can send the same prompt
down each and see what actually differs.

It also does the two things `curl` will not:

- **Keeps multi-turn correct.** With tools in play, DeepSeek returns
  `400` unless every assistant message's `reasoning_content` is replayed
  on every later request. `--continue` handles that; without tools it
  strips the same field, because replaying it there just burns tokens.
- **Prices every call.** DeepSeek's disk KV-cache makes a cached input
  token **50× cheaper** than an uncached one. That split is invisible
  unless something reads `prompt_cache_hit_tokens` and does the
  arithmetic. This does, on every call, and keeps a local ledger.

```console
$ deepseek chat "explain this diff" --file changes.patch
The patch swaps the retry loop for exponential backoff...
· flash · 3.2k in (87% cached) · 412 out (180 think) · ~$0.000178 · 2.1s
```

## Install

**One line** — detects your platform, installs the binary and both
aliases:

```bash
curl -sL https://raw.githubusercontent.com/thevibeworks/deepseek-cli/main/install.sh | sh
```

**Or download a binary** from [Releases](https://github.com/thevibeworks/deepseek-cli/releases):

```bash
# macOS (Apple Silicon)
curl -sL https://github.com/thevibeworks/deepseek-cli/releases/latest/download/deepseek_darwin_arm64.tar.gz | tar xz
sudo mv deepseek /usr/local/bin/

# Linux (x86_64)
curl -sL https://github.com/thevibeworks/deepseek-cli/releases/latest/download/deepseek_linux_amd64.tar.gz | tar xz
sudo mv deepseek /usr/local/bin/
```

**Or with Go:**

```bash
go install github.com/thevibeworks/deepseek-cli/cmd/deepseek@latest
```

### `ds` and `dscli`

`deepseek` is eight characters to type many times a day. The binary
answers to `ds` and `dscli` as well — not shell aliases, but symlinks to
the same binary, so they work in scripts, `cron`, `Makefile`s and
anywhere a shell alias would not:

```bash
ds chat "why is the sky blue"
ds usage --since 7d
```

`install.sh` and `make install` create them. If you installed by hand:

```bash
sudo ln -sf deepseek /usr/local/bin/ds
sudo ln -sf deepseek /usr/local/bin/dscli
```

The binary notices which name invoked it, so `ds --help` says `ds`.

## Quick start

```bash
export DEEPSEEK_API_KEY=sk-...          # https://platform.deepseek.com/api_keys
deepseek check                          # is everything reachable?
deepseek chat "why is the sky blue"
```

`check` calls all six endpoints once and reports which answered. Run it
first when something is wrong and you do not yet know whether the problem
is the key, the balance, the network, a proxy, or one endpoint:

```console
$ deepseek check
https://api.deepseek.com

ok    GET /models                  141ms  deepseek-v4-flash, deepseek-v4-pro
ok    GET /user/balance            126ms  18.48 CNY
ok    POST /chat/completions       651ms  5 in / 1 out
ok    POST /anthropic/v1/messages  590ms  5 in / 1 out
ok    POST /responses              595ms  5 in / 9 out
ok    POST /beta/completions       379ms  4 in / 1 out

all endpoints reachable
```

## Commands

One command per endpoint, named for what it does.

| Command | Endpoint | What it is for |
| --- | --- | --- |
| `chat` | `POST /chat/completions` | The default. OpenAI format, the one most tools speak. |
| `anthropic` | `POST /anthropic/v1/messages` | The format Claude Code and the Anthropic SDKs speak. |
| `respond` | `POST /responses` | The format Codex speaks. JSON Schema output and server-side `web_search` live only here. |
| `fim` | `POST /beta/completions` | Fill in the middle — the shape editors use for inline completion. |
| `models` | `GET /models` | Available models, joined with the published rate card. |
| `balance` | `GET /user/balance` | What is left, per currency. |
| `usage` | *(local)* | What this CLI has spent, from its own ledger. |
| `session` | *(local)* | The conversations `chat --continue` replays. |
| `check` | *(all six)* | Preflight. |
| `raw` | *(anything)* | Escape hatch — any path, with auth and retries. |

### Talking to models

```bash
deepseek chat "why is the sky blue"
git diff | deepseek chat "write a commit message"
deepseek chat "explain" --file server.go --file server_test.go
deepseek chat "review this" --model deepseek-v4-pro --effort max
deepseek chat "summarise" --system @house-style.md
```

Arguments are the instruction, pipes and `--file` are the material.
Answers stream to stdout; the chain of thought, the usage line and any
warnings go to stderr — so redirecting stdout gets you the answer alone.

### Multi-turn

The API is stateless in every format. Conversations live on your machine.

```bash
deepseek chat "walk me through this codebase" --file main.go --continue
deepseek chat "now what would you change first"   --continue
deepseek session ls
deepseek session show last
```

Use `--session <name>` to keep threads apart. `--continue` is the session
named `last`.

### The other three formats

```bash
# Anthropic Messages. Claude model names are accepted and remapped
# server-side; the usage line shows both so cost stays traceable.
deepseek anthropic "hello" --model claude-opus-4-1
# · claude-opus-4-1→pro · 10 in · 8 out · ~$0.000011 · 0.9s

# Responses: JSON Schema output, and a web_search tool DeepSeek runs
deepseek respond "what shipped in Go 1.26" --web-search
deepseek respond "Berlin" -s "Return city and country." --schema @city.json

# FIM: prefix in, suffix optional, the middle comes back
deepseek fim "def add(a, b):" --suffix "    return result"
```

### Tools

Declare a tool once; it works against every format. Both the wrapped
OpenAI shape and the bare `input_schema` Anthropic shape are accepted.

```bash
deepseek chat "weather in Hangzhou?" --tool @weather.json
# tool_call call_00_hUj... get_weather({"city": "Hangzhou"})

deepseek anthropic "weather in Hangzhou?" --tool @weather.json   # same file
```

This prints the calls the model wants made. It does not run them —
executing model-chosen commands is an agent's job and a much larger set
of safety questions. This is for developing and debugging tool schemas.

### Raw

```bash
deepseek raw /models
deepseek raw /chat/completions --data @request.json
```

Every other command is a typed convenience over this one, so anything
DeepSeek ships tomorrow is reachable today.

## What things cost

Every call prints a usage line to stderr and appends a row to a local
JSONL ledger:

```console
$ deepseek usage --since 7d
                   CALLS  IN     CACHED  OUT    COST
deepseek-v4-flash  184    2.1M   78%     94k    $0.19
deepseek-v4-pro    12     88k    41%     11k    $0.03
total              196    2.2M   77%     105k   $0.22

by format: chat 170, anthropic 14, responses 8, fim 4
context cache saved ~$0.23 (1.7M of 2.2M prompt tokens replayed)
costs are estimates from the published USD rate card, not billed amounts
```

That last line is the point. Cached input costs $0.0028/M against
$0.14/M for a miss — structuring prompts so the stable part comes first
is worth real money, and this is how you see whether it worked.

Honest limits:

- Costs are **estimates** from the published USD rate card, not billed
  amounts. Token counts are exact, and they are what the ledger stores,
  so old rows can be repriced when the card changes.
- DeepSeek has announced peak/off-peak pricing (2× during 09:00–12:00
  and 14:00–18:00 Beijing time) with **no effective date**. It is
  deliberately not applied — guessing that a call was billed double
  would be inventing data.
- `--no-ledger` skips the write; `--no-stats` hides the line.

## For agents

Built to be scripted. See [AGENTS.md](AGENTS.md) for the full contract
and [skill/SKILL.md](skill/SKILL.md) for a drop-in agent skill.

```bash
deepseek chat "..." --json | jq -r '.choices[0].message.content'
deepseek chat "..." --jq '.usage'
deepseek models --json
```

- **stdout is data, stderr is status.** Safe to pipe.
- **`--json` prints the API's own response**, unwrapped and unmodified,
  so jq recipes written against the OpenAI or Anthropic APIs keep working.
- **Exit codes carry meaning:** `0` ok · `1` error · `2` auth · `3` no
  balance · `4` rate limited · `130` interrupted.
- **Errors say what to do**, not just what broke:

```console
$ deepseek chat hi
Error: Insufficient Balance (HTTP 402)
  out of balance: deepseek balance — top up at https://platform.deepseek.com/top_up
```

## Configuration

| Variable | Purpose |
| --- | --- |
| `DEEPSEEK_API_KEY` | API key. |
| `DEEPSEEK_BASE_URL` | Override the base URL (proxies, gateways). |
| `DEEPSEEK_CONFIG_DIR` | Where the key file lives. Default `~/.config/deepseek`. |
| `DEEPSEEK_STATE_DIR` | Ledger and sessions. Default `~/.local/state/deepseek`. |

Both directories respect `XDG_CONFIG_HOME` / `XDG_STATE_HOME`. To keep
the key out of the environment, put it in a file instead:

```bash
mkdir -p ~/.config/deepseek && chmod 700 ~/.config/deepseek
printf 'sk-...' > ~/.config/deepseek/api_key && chmod 600 ~/.config/deepseek/api_key
```

Global flags: `--api-key` `--base-url` `--json` `--jq` `--timeout`
`--verbose/-v` (`-vv` adds bodies) `--no-stats` `--no-ledger`.

## Good to know

Things the API does that surprise people, and that this tool surfaces
rather than hides:

- **Thinking is on by default** and costs a flat 79 extra input tokens
  per request for its template — measured, and constant regardless of
  prompt length — before a single reasoning token is generated. On short
  factual work that is most of the bill; `--think off` removes it.
- **Text only.** DeepSeek rejects image, document and search-result
  content blocks in every format.
- **The Responses endpoint is flash-only** for now.
- **FIM caps output at 4K tokens** and ignores thinking entirely.
- **Slow starts are normal.** The API holds the connection with
  `: keep-alive` comments for up to ten minutes before inference begins.
  The default `--timeout` matches.

## Development

```bash
make            # build
make check      # fmt + vet + test + build
make test       # 102 tests, no network required
make cover-gate # fails under the coverage floor
```

Zero runtime dependencies beyond `cobra` and `golang.org/x/term`; the API
client is hand-rolled. `--jq` shells out to `jq` if you use it.

Design rulings and the reasons behind them: [TASTE.md](TASTE.md).

## License

MIT — see [LICENSE](LICENSE).
