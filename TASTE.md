# TASTE — design scars

Prior design rulings for deepseek-cli. One entry per real rejection, each
with its why — a rule without a why fossilizes into style police. Read
this before any design verdict; delete a scar when its expiry condition
arrives.

The test behind all of them: a surface that gets prettier while the task
gets harder is a costume, and it fails no matter how clean it looks in a
screenshot.

---

## 2026-08-05 rejected: one `ask` verb with `--api` to pick the format

**Why.** DeepSeek's four wire formats all answer "talk to the model", so
the tidy design is one command with a `--api chat|anthropic|responses`
switch. It collapses on contact with the response. The three formats
return genuinely different objects — `choices[].message` versus
`content[]` blocks versus `output[]` items — and different usage shapes
with opposite cache conventions. A unified command has two ways out and
both are bad: normalize, and `--json` lies about what the API sent; or
leak, and one command emits three shapes depending on a flag. Meanwhile
format-specific parameters (`web_search` and JSON Schema only exist on
Responses, `prefix` only on beta chat) have to hide behind conditional
validation. One verb, four behaviours, and the user still has to know
which format they are in — the surface got smaller while the task got
harder.

**Reuse.** Sibling commands per format, sharing a request-building core
underneath. `chat` is the default door and gets the recommendation in
`--help`; the others are named for who speaks them (Claude Code, Codex)
so the choice is about the ecosystem, not the endpoint. Same shape as the
underlying API means `--json` never has to lie.

**Expires.** If DeepSeek ever converges the response shapes — same object
from every path — the merge becomes honest and this should be revisited.

---

## 2026-08-05 rejected: wrapping the API response to attach cost

**Why.** Cost per call is the feature this tool exists for, so the first
design put it in the output: `{"response": {...}, "stats": {...}}`.
That breaks every jq recipe anyone has ever written against the OpenAI or
Anthropic APIs — `.choices[0].message.content` becomes
`.response.choices[0].message.content` for no reason the caller asked
for. Worse, it makes `--json` a claim about our envelope rather than
about the API, which is the one thing `--json` should never be.

**Reuse.** `--json` prints the API's bytes, unwrapped and unmodified.
Cost goes two other places: a one-line human summary on stderr, and an
append-only ledger that `deepseek usage` reports over. That split turned
out better than the wrapper anyway — "what did I spend today" is a
question about a week of calls, not about one response object.

**Expires.** Not expected to. If a caller ever genuinely needs per-call
cost in a pipeline, `usage --entries --json` already answers it without
touching the response shape.

---

## 2026-08-05 rejected: catching SIGTERM without a way to still die

**Why.** Ctrl-C during a streamed answer should unwind cleanly — keep the
text already on screen, print the usage line — so the obvious move is
`signal.NotifyContext` over SIGINT and SIGTERM. But catching a signal
replaces the default handler, and the default handler is what makes
`timeout` and `kill` work. If the work does not stop when the context is
cancelled — blocked on a read, or writing to a pipe nobody is draining —
the process becomes unkillable by ordinary means. Caught during testing
when `timeout 20` failed to end a run. A graceful shutdown that can hang
forever is strictly worse than an abrupt one.

**Reuse.** Catch the signal, cancel the context, and arm a watchdog: the
second signal, or two seconds, force-exits regardless. If you take
responsibility for a signal, take responsibility for still dying.

**Expires.** Not expected to.

---

## 2026-08-05 rejected: applying the announced peak-hour price multiplier

**Why.** DeepSeek has announced peak/off-peak pricing — 2× on all billing
items during 09:00–12:00 and 14:00–18:00 Beijing time — but the docs say
the effective date is "subject to the official announcement". Encoding it
was tempting because the code is three lines and it would make the
estimate "more accurate later". It would in fact make every current
estimate wrong by a factor of two, silently, based on a clock. Guessing
that a call was billed double is inventing data and calling it precision.

**Reuse.** Price at the published rates only. Where a figure is an
estimate, label it one and store the exact token counts beside it so any
row can be repriced later. The rate table carries a comment saying where
the multiplier goes when it lands.

**Expires.** When DeepSeek announces the effective date — then the
multiplier goes in, gated on that date.

---

## 2026-08-05 rejected: executing the tool calls the model asks for

**Why.** `--tool` makes the model emit `tool_call` objects, and running
them is a small amount of code away — obviously useful, obviously what
someone will ask for. It is also the line between a CLI and an agent
runtime. Executing model-chosen commands brings sandboxing, approval,
audit, and a failure mode where a bad prompt runs `rm -rf`. Shipping half
of that inside a tool whose job is "send a request, print the response"
would make the tool harder to trust and would not produce a good agent.

**Reuse.** Print the tool calls to stderr; do not run them. The stated
job is developing and debugging tool schemas, and it does that fully.
Agents that need execution have their own loop and can call this for the
one turn.

**Expires.** Not expected to. A tool-executing mode would be a different
product, not a flag.
