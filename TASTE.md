# TASTE — design scars

Prior design rulings for deepseek-cli. One entry per real rejection, each
with its why — a rule without a why fossilizes into style police. Read
this before any design verdict; delete a scar when its expiry condition
arrives.

The material these rulings are about is in `DESIGN.md`.

The test behind all of them: a surface that gets prettier while the task
gets harder is a costume, and it fails no matter how clean it looks in a
screenshot.

---

## 2026-08-07 rejected: rebuilding the sea in three.js

**Why.** The obvious way to make a page about depth feel deep is WebGL: a
real volume, real caustics, real particles, and three.js is right there.
The cost is not the frame budget, it is everything the site currently is.
`waves.js` has no dependencies and no build step, it reads every colour it
paints from a CSS custom property so both themes live in the stylesheet,
its star and snow fields are seeded so a rendered frame is reproducible
and testable, and the whole thing degrades to a CSS gradient and an SVG
whale with JavaScript off. three.js is ~600KB before a line of ours, needs
a bundler on a site whose pitch includes not having one, cannot read the
theme without a colour bridge written by hand anyway, and turns a
deterministic frame into something no test can assert about. It would buy
a better-looking sea and sell the reasons the sea is good.

**Reuse.** The 2D canvas already had the right substrate — four sine
layers with a nearness gradient, a whale reading its height out of the
same field. Depth came from making those layers respond to scroll
position, which is arithmetic on numbers that were already there: about
sixty lines, no new dependency, and the descent is testable because the
fields are still seeded. Parallax, an abyss veil and marine snow do the
job WebGL was wanted for, at a few hundred sine calls a frame.

**Expires.** If the site ever grows something that genuinely needs a
scene graph — a 3D model of something, real lighting — this is worth
revisiting for that thing, on that page, not for the background.

---

## 2026-08-07 rejected: a scroll-position effect that is only an effect

**Why.** "Scrolling should feel like going deeper" is a mood, and the
first version of it was exactly that: the water darkened as you scrolled
and nothing else was true. It photographs well and it tells the reader
nothing they did not already know — which is the definition of the costume
this file exists to catch. On a docs page long enough to want the effect,
the reader's actual question is *how much of this is left*, and the native
scrollbar autohides on every platform that matters.

**Reuse.** The descent publishes one number, `--depth`, and the same
number is legible as text in the margin the 68ch measure was already
wasting. It is the scroll position — the thing the scrollbar shows — said
in the page's own vocabulary and left on screen. That is the difference
between an effect and an instrument: the effect makes you feel deep, the
instrument tells you how deep. We shipped the second one and got the first
for free.

**Expires.** If the gauge ever has to move over the text column to fit, it
has stopped being free and should be deleted rather than shrunk.

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

**Expired 2026-08-13.** DeepSeek dated it: peak/off-peak billing on a new
card from 2026-08-16 16:00 UTC, windows defined in UTC (01:00–04:00 and
06:00–10:00). The multiplier went in exactly as this entry prescribed —
gated on the effective instant, never applied before it. The schedule
lives in `internal/deepseek/pricing.go`, mirrored by the gateway meter and
the site's pricing page; `deepseek pricing` prints it. The principle
stands for the next undated announcement.

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

---

## 2026-08-05 rejected: scraping status.deepseek.com

**Why.** `deepseek status` should obviously report DeepSeek's status, and
the status page is right there. But it is a client-rendered app with no
documented JSON endpoint — no `/api/v2/summary.json`, no Instatus shape,
nothing to parse but markup nobody promised to keep. A scraper written
against that markup does not fail loudly when a `div` is renamed; it
fails by reporting **all systems operational** forever. A wrong all-clear
is worse than no answer, because it is the answer people act on at 3am.

**Reuse.** `status` answers the question the user actually has — is the
API reachable *right now, with this key, from this machine* — with two
calls that generate no tokens, so it costs nothing and is safe in a loop.
That is also a strictly better question than the status page answers: a
working API behind a broken corporate proxy looks fine there and broken
here. The incident page is linked, in the text output and in the JSON, so
a human always has somewhere to go.

**Expires.** If DeepSeek publishes a documented status JSON endpoint,
consume it and report both.

**2026-08-07 — expiry tested, still holds, and now for a second reason.**
Probed for a JSON endpoint from the production host: `/api/v2/summary.json`,
`/api/v2/status.json`, `/api/status`, `/api/v1/status` and the bare page all
fail. `status.deepseek.com` resolves to an Aliyun Beijing load balancer
(`statuspage.flashcat.cloud`) that accepts the TCP connection and then never
completes the TLS handshake from outside China, while `api.deepseek.com`
answers fine from the same box. So the page is both unparseable and
unreachable from where our gateway runs. Consuming it would mean shipping a
health signal that is permanently unknown — worse than the wrong all-clear
this scar was written about, because it would also be *our* dashboard showing
red for *their* geography. What the gateway publishes instead is a
first-party observation: every round trip to `api.deepseek.com` is recorded,
and `/v1/status` carries an `upstream` block with the last success, the
consecutive-failure streak and the last fault shape. That answers the
question a visitor actually has — is this you or them — with data a status
page structurally cannot have, since it cannot see a route that is broken
only from here. The incident page stays linked for humans.

---

## 2026-08-05 rejected: shipping the tokenizer, or estimating from the ratios

**Why.** Counting tokens locally means one of two bad trades. Porting
DeepSeek's demo tokenizer — a Python zip, still labelled *v3* while the
API serves v4 — means several hundred lines of BPE in Go, pinned to a
vocabulary we cannot verify matches what bills us, and stale the moment
they retrain. Using the published rules of thumb (0.3 tokens per English
character, 0.6 per Chinese) is honest but wrong: measured, the English
ratio over-counts by about 30% on prose. A CLI whose whole pitch is exact
accounting cannot headline an approximation.

**Reuse.** Ask the API, from an endpoint nobody uses for this. FIM at
`/beta/completions` takes a raw prompt with no chat template around it
and reports `prompt_tokens` for exactly the bytes sent, plus one BOS
token — verified constant from 1 character to 1,800. Subtract the one and
the count is exact for the tokenizer that will actually bill you. The
cost of measuring is printed every time, because the text really is sent
and really is billed. `--offline` keeps the ratio estimate for when free
matters more than right, labelled an upper bound.

**Expires.** If DeepSeek ships a count-tokens endpoint, or a versioned
tokenizer matching the served model, switch to it.

---

## 2026-08-05 rejected: making `--interactive` the default when stdin is a terminal

**Why.** `deepseek chat "hi"` at a terminal could reasonably drop into a
prompt for follow-ups — the information is all there, and the guard
(`isTTY(stdin) && isTTY(stderr)`) is reliable. It is still wrong. A
one-shot command that sometimes does not exit breaks the contract every
other invocation relies on: `time`, `&&`, a `Makefile` rule, a shell
loop, a person who typed one question and wants their prompt back. The
convenience is worth one character; the surprise is worth a bug report.

**Reuse.** `-i` opts in, and it is built on the session machinery rather
than beside it — so leaving the loop loses nothing, `chat -c` resumes it,
and `session show last` reads it. Interactive mode adds a prompt, not a
second way to hold a conversation.

---

## 2026-08-05 rejected: requiring every search term to appear on the page

**Why.** Strict AND is the obvious retrieval rule and it reads as
precision. It shipped a wrong answer within an hour. Asked *"what is the
max output token limit for FIM"*, it excluded `guides/fim_completion` —
the one page that states the 4K cap — because that page never uses the
word "output". The model was then handed release notes, correctly refused
to invent, and reported that the documentation does not say. It does say.

**Reuse.** Coverage weights rather than gates: `score × (matched/total)²`,
over BM25 term saturation with length normalisation, times an IDF weight
per term. Each of those three fixed a real failure — coverage the one
above; length normalisation an 82KB integration page that won any query
containing an ordinary English word; IDF the fact that in "max output
token limit for FIM" only one word identifies the subject. All five cases
are pinned in `internal/docs/docs_test.go` against the real corpus.

---

## 2026-08-05 rejected: light-theming the terminal transcripts

**Why.** The site's terminal blocks are page furniture like everything
else, so the tidy rule is that they follow the theme. But they are not
furniture — they are a picture of a terminal, and the colours inside them
are the ANSI-ish set the CLI actually prints, calibrated against a dark
background. Recolouring them for paper makes the screenshot a lie about
what the tool looks like. A photograph does not invert when the page
does.

**Reuse.** `.term` carries its own fixed palette and `color-scheme: only
dark`, in both themes. Print is the exception — on paper it is text, not
a screen, so `@media print` flips it back.

---

## 2026-08-05 rejected: a sun/moon theme toggle

**Why.** Two icons can only express two states, so the moment anyone
touches it, "follow the system" becomes unreachable — and following the
system is the correct default for almost everyone. The usual patch is a
long-press or a settings panel, which is more machinery than the whole
feature deserves.

**Reuse.** On a site set in a monospace face, the honest control is the
word: a button reading `theme: auto` that cycles auto → light → dark and
names the state it is in. Three states, no legend, no icon vocabulary to
learn. It ships `hidden` and JS unhides it, because a control that cannot
work without JS should not take up space when there is none.
## 2026-08-05 rejected: making per-user quota the thing that protects the free tier

**Why.** The obvious design for a keyless free tier is a good identity
system: proof-of-work tokens, IP buckets, escalating difficulty, maybe a
GitHub sign-in — and then a generous per-user quota enforced against it.
Every one of those pieces is worth having and not one of them bounds the
loss. Identity on the open internet is not something you win. A rented
box farms proof-of-work overnight, a $5 proxy pool defeats IP bucketing,
and both are cheaper than the credits they drain. A design whose spending
limit is `per_user_quota × number_of_identities` has no limit, because
the right-hand term is chosen by the attacker.

**Reuse.** A global daily budget that does not care who you are. It fires
on total spend and refuses everyone, including users who have used
nothing. Per-user quota stays, demoted to what it is good at — fairness
between honest people — and the identity layer only has to be good enough
to stop casual abuse, which is exactly what proof-of-work is good enough
for. The honest cost is stated where it lands: a determined attacker can
burn the day's budget in an hour and everyone else gets "come back
tomorrow". We can survive a bad day; we cannot survive a bad invoice.

**Expires.** If the free tier ever gets an identity with real cost behind
it — a payment method on file, a verified account — the per-identity
limit becomes load-bearing and the breaker can be raised.

---

## 2026-08-05 rejected: charging nothing for a response we could not meter

**Why.** The gateway reads token counts out of DeepSeek's responses to
bill the budget. The natural failure branch is "no usage object found →
charge zero", and it is a hole straight to the credit pool: whatever
makes a response unparseable becomes the cheapest way to use the service,
and it is not a hole an attacker has to find deliberately. A new field,
a truncated stream, a format we have not seen — each one silently turns
into free inference.

**Reuse.** Unbillable is charged a deliberate over-estimate: the request
body at four bytes per token, plus the full `max_tokens` ceiling the
gateway already clamped it to. It is provably above what the request
could have cost, and the journal marks the row `estimated` so an operator
can tell defensive spend from real spend when the two diverge. The same
rule decides the rate card for an unknown model — pro's, the expensive
one, so a model we have not priced over-charges our own budget rather
than under-charging it.

**Expires.** Never, while we are the ones paying.

---

## 2026-08-05 rejected: silently downgrading a pro request to flash

**Why.** The free tier serves flash. A request for `deepseek-v4-pro`
could be quietly answered by flash, which keeps the request working and
costs us a third as much — and that is the trap. The user compares what
came back against pro's reputation and concludes the model is worse than
it is. We would have spent their trust in DeepSeek to save our own money,
which is a bad trade at any exchange rate.

**Reuse.** Refuse it, name the model that was asked for, and say where a
key that can serve it comes from. The one adjacent case where a default
is changed rather than refused is `deepseek fim`, whose *own* default is
pro: there the user did not ask for anything, so the CLI moves its
default to what the transport serves and says so on stderr. The rule is
that an explicit choice is never overridden, only a default the user
never made.

**Expires.** If the free tier ever carries pro, this goes away with it.

---

## 2026-08-05 rejected: answering /models locally on the gateway

**Why.** The gateway is a transparent proxy, so the tidy version of
"only advertise the model we serve" is to answer `GET /models` from
config and never call upstream. It saves a round trip and it breaks
`deepseek status`, whose entire job is answering "is DeepSeek reachable
from here" — against a locally-answered `/models` it would report the
gateway's health and call it the API's.

**Reuse.** Proxy the call, filter the response. It is the one place the
gateway edits a body, and it earns the exception because `/models`
exists to answer "what can I use here" and an unfiltered list gives any
client picking off it even odds of choosing the model that is then
refused. Anything unparseable passes through untouched.

**Expires.** If the free tier ever serves every model, the filter becomes
a no-op and should be deleted rather than left to rot.

---

## 2026-08-05 rejected: sharing the proof-of-work code between the client and the gateway

**Why.** The same puzzle is solved by the CLI, solved again by the
browser playground, and verified by the gateway. Three implementations of
one hash construction is exactly the duplication a shared package exists
to remove — and the package would have to be imported by a Go module
whose two-dependency footprint is part of its pitch, by a second Go
module that deliberately has none, and by a browser that cannot import Go
at all. The only version that works for all three is vendored copies
pretending to be a library.

**Reuse.** Three independent implementations, one shared table of test
vectors, checked into all three test suites: the smallest nonce solving a
fixed challenge at a fixed difficulty. A change to any hash construction
fails on every side at once, which is the property the shared package was
wanted for. The browser copy additionally checks itself against node's
`crypto`, because "internally consistent but wrong" would pass the
vectors by coincidence and fail against the gateway every time.

**Expires.** If the CLI and the gateway ever merge into one module, the
Go halves should share code and only the browser stays separate.

---

## 2026-08-07 accepted, after a measurement: `web_search` on the free tier

Recorded here because it reverses an earlier refusal, and a reversal
without its evidence is just a mood swing.

**The refusal it replaces.** `policy.forbidServerTools` rejected every
server-side tool with a real argument: such a tool "performs billed work
that never appears in the usage object, which would put its cost outside
every ceiling this gateway enforces". Correct reasoning, untested premise.

**What the measurement showed.** One `respond --web-search` call against
the live API on 2026-08-07 made eleven server-side calls (searches, page
opens, an in-page find) and reported 40,260 input tokens, 32,000 of them
cache hits, 3,100 output. The account balance moved by nothing beyond
those tokens — eleven searches at a frontier vendor's $10-per-1,000 rate
would have been $0.11, or 0.79 CNY, and unmistakable against a 14.26 CNY
balance. So the premise was wrong in the half that mattered: there is no
per-search fee, and the entire cost of a search arrives as input tokens
that this gateway already meters exactly.

**What the measurement did break.** The half of the premise that was
right, in a different place than expected. `meter.Estimate` bounded a
request's input at one token per body byte — true for every other request
and false for this one, because DeepSeek chooses how many pages to read.
A search request's input is upstream-controlled, so the admission
reservation no longer bounds it from the body.

**Reuse.** Allow `web_search` (both the bare and dated tool names), refuse
every other server-side tool still, and pay for the change in two places:
a 256k-token input allowance in the reservation, roughly six times the
observed case; and a per-user daily ration of three searches, which is a
new quota dimension because one search costs about what ten ordinary turns
cost and the request counter alone would let one caller quietly take a
quarter of the day's budget. Stating the trade honestly: within the
allowance the budget is still a hard ceiling, and beyond it a search can
overshoot by the difference, bounded by the few distinct callers who can
be mid-search at once given a per-subject in-flight cap of one.

**Expires.** If DeepSeek starts billing searches separately, or documents
a cap on injected search context, redo the arithmetic — the allowance and
the ration are both sized to a single measurement and should be re-measured
when the tool changes. If a search request is ever observed above 256k
input tokens in production, that is the signal to raise the allowance
rather than to quietly accept the overshoot.
