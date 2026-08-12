# dsgate

The free tier for `deepseek`: a metered proxy that lets people use the
DeepSeek API before they have an API key.

```bash
deepseek free                      # ~1s of CPU, no account
deepseek chat "why is the sky blue"
```

That is the whole enrolment. No email, no card, no dashboard. The
gateway holds one real API key, relays requests to DeepSeek, meters every
token, and stops when the day's budget is gone.

- **[DESIGN.md](DESIGN.md)** — why it is shaped this way, what it refuses
  to do, and the reasoning behind every limit. Read this before changing
  anything here.
- **[deploy/README.md](deploy/README.md)** — running one.

## The shape of it

```
                                       chat ─→ opencode.ai/zen  (free, flaky)
  deepseek CLI  ─┐                       ↑         │ refused
                 ├─→  dsgate  ───────────┴─────────┴──→  api.deepseek.com
  playground    ─┘     │                                   (our key + user_id)
   (browser)           └── journal.jsonl   counts only, never prompts
```

It is a **policy-enforcing transparent proxy**, not a reimplementation of
the API. The CLI already speaks four wire formats against one
configurable base URL, so a faithful proxy makes `chat`, `anthropic`,
`respond`, `fim` and `models` all work through it unchanged — and so will
whatever DeepSeek ships next month.

What it changes about a request, and nothing else:

| | |
|---|---|
| `model` | pinned to flash; a pro request is **refused**, not downgraded |
| `max_tokens` | clamped to the free-tier ceiling |
| `n`, `best_of` | refused above 1 — they multiply the cost of one admitted request |
| user identity | overwritten with the token's subject |

That last one is not a nicety. DeepSeek documents `user_id` as the
mechanism for one account fronting many end users, and it buys content
safety attribution, **KV cache isolation between strangers**, and
per-user scheduling. It is overwritten rather than honoured because a
client that could choose its own could aim at someone else's cache
namespace.

## The free lane

Set `OPENCODE_API_KEY` and chat requests go to [OpenCode
Zen](https://opencode.ai/docs/zen)'s `deepseek-v4-flash-free` first, which
costs this service nothing. Anything it refuses falls through to the
DeepSeek key before the caller sees a byte, so the worst case is one extra
round trip — measured at ~0.65s for a refusal against ~1.8s for an answer.

Measured against Zen on 2026-08-12, which is why the lane is this narrow:

| | |
|---|---|
| refusal rate | ~20% of sequential requests, `429 FreeUsageLimitError` |
| `/chat/completions` | works; usage reported streamed and buffered |
| `/responses` | answers, but rejects a server-side `web_search` tool |
| `/anthropic/v1/messages`, `/beta/completions`, `/user/balance` | 404 |
| the model's name there | `deepseek-v4-flash-free`, aliased at the last moment |
| privacy | Zen says free-lane data **may be used to improve the model** |

So it carries `chat` and nothing else. FIM, the Anthropic and Responses
formats, web search and the model list all go straight to DeepSeek, and
the caller's contract does not change: they ask for `deepseek-v4-flash`,
by that name, on every route.

The interesting consequence is what happens when the money runs out.
A request that the free lane can serve is admitted **past** the daily
budget and the credit pool, because it cannot spend either — so the day's
budget being gone downgrades the service to chat-on-the-free-lane
(`state: free_upstream_only`) instead of ending it until 00:00 UTC. Such
a request is bound to that one upstream and is never retried against the
paid key; that invariant is what makes skipping the ceiling sound.

## What bounds the spend

Not the per-user quota. Identity on the open internet is not something
you win, and a limit of `quota × identities` has no limit when the second
term is the attacker's to choose.

What bounds it is a **global daily budget** that does not care who you
are, plus a lifetime credit pool. Per-user quota is fairness between
honest people; proof-of-work makes casual farming cost something. Neither
is the security boundary, and the design does not pretend otherwise.

## Layout

```
cmd/dsgate/          configuration, signals, the listener
internal/token/      credential codec — challenges and bearer tokens
internal/mint/       issuing tokens: difficulty policy, replay, IP buckets
internal/quota/      the money: counters, budgets, the spend journal
internal/meter/      reading usage out of four wire formats, and pricing it
internal/policy/     which requests are carried, and how they are rewritten
internal/server/     HTTP: the mint endpoints and the proxy
```

A separate Go module from the CLI, with **no dependencies at all** — the
CLI's dependency list is part of its pitch and a server has no business
widening it. No database either: counters live in memory and every debit
is appended to a JSONL journal replayed at boot, which is enough for
hundreds of requests a day and keeps the whole thing one static binary
small enough to sit on a 1 GiB box next to someone else's production.

## Tests

```bash
make gateway-test     # unit, plus a real CLI binary against a real gateway
make price-check      # the rate card exists in two modules; this catches drift
```

The interop test is the one that matters. The CLI and the gateway are
separate modules that share a documented wire format and no code, so the
only proof they agree is running the actual `deepseek` binary through the
actual gateway. The browser playground is a third implementation of the
same enrolment protocol; all three are pinned to one shared table of
proof-of-work vectors.
