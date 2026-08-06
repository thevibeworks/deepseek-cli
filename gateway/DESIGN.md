# dsgate — a free tier for `deepseek`

> Why this exists, what it refuses to do, and the reasoning behind each
> choice. Read this before changing anything in `gateway/`.

The pitch is one line: **`deepseek chat "hi"` works before you have an API
key.** No signup, no card, no email. Type `deepseek free` once and the CLI
works.

Nothing else in the DeepSeek ecosystem does this. It is the distinctive
thing. It is also the thing that spends real money on strangers' behalf,
so the whole design is about making that bounded.

---

## Frame

**The decision:** how to give anonymous users real API access on our
credits without letting anyone drain them.

**Fixed constraints:**

- No signup. The moment there is a signup, the magic is gone and we are
  just another dashboard.
- We pay per token, upstream, in advance. Credits are finite and the
  refill is a human with a credit card.
- One 1 GiB shared VPS (`oci_micro_20231229_1513`, San Jose) already
  running RSSHub, Redis, browserless, nginx, Tailscale and two Xray
  stacks. Whatever we add has to be small and quiet.
- The CLI already speaks four wire formats against one configurable base
  URL. Anything that breaks that is a regression.

**What good looks like**, in priority order:

1. **Bounded spend.** A hostile user with a botnet must not be able to
   cost us more than the daily budget. This is the only hard requirement;
   everything else is negotiable.
2. **Zero friction for the honest user.** One command, no account.
3. **Honest about the trade.** The user is sending prompts through our
   box. They must know that before the first byte leaves.
4. **Small.** It runs next to other people's production services.

---

## Spread

### 1. Source-IP rate limiting only

- **What:** No identity at all. Bucket by IP and hope.
- **Upside:** Fifty lines. Nothing to store, nothing to mint, no client
  changes beyond a base URL.
- **Cost:** A $5/mo proxy pool or a single IPv6 /48 defeats it entirely,
  and one university NAT shares one quota between two thousand students.

### 2. Proof-of-work minted device tokens

- **What:** The client burns CPU once to mint a bearer token. Quota is
  keyed to the token, not the connection.
- **Upside:** Makes identity *cost* something without asking for
  anything personal. Survives NAT — every student on the campus mints
  their own.
- **Cost:** CPU is cheap. A spot instance mints thousands of identities
  overnight. PoW alone buys maybe 100x, not 10,000x.

### 3. GitHub device flow, no anonymous tier

- **What:** "No API key" becomes "no *DeepSeek* API key" — sign in with
  GitHub in five seconds, get a real identity with an account age we can
  check.
- **Upside:** The strongest identity signal available for free, and it
  makes per-user abuse attributable and revocable.
- **Cost:** It is still a signup wall. It kills the exact property —
  install and it works — that the whole feature exists to deliver.

### 4. A demo drip, not a service

- **What:** Five messages per IP, no identity, no state. A taste, then
  "bring your own key".
- **Upside:** Near-zero abuse exposure and near-zero code.
- **Cost:** Five messages is a marketing gimmick, not something anyone
  tells a friend about.

### 5. Layered ladder with a global circuit breaker

- **What:** Anonymous PoW token gets a small daily quota; a global daily
  budget caps everyone's total; when lifetime credits run out the service
  says so and points at BYO-key or a subscription.
- **Upside:** The per-user layer can be defeated and the spend is *still*
  bounded, because the budget breaker does not care who you are.
- **Cost:** Three limits to reason about instead of one, and a hostile
  user can spend the whole day's budget before honest users wake up.

---

## Commit

| | bounded spend | friction | honesty | size |
|---|---|---|---|---|
| 1. IP only | no — trivially bypassed | none | fine | tiny |
| 2. PoW tokens | partly — raises cost ~100x | one-time ~1s CPU | fine | small |
| 3. GitHub | yes — identity is costly to farm | signup wall | fine | medium |
| 4. Demo drip | yes — nothing to drain | none | fine | tiny |
| 5. Ladder + breaker | **yes — by construction** | one command | needs disclosure | medium |

**We build 5**, with 2 as its anonymous rung and 3 designed in as the
upgrade rung but not built yet.

The reasoning that decides it: approaches 1–3 all try to make *identity*
trustworthy, and identity on the open internet is not something you can
win. Approach 5 stops trying. The per-identity quota is a fairness
mechanism, not a security mechanism. The security mechanism is a budget
that cannot be exceeded no matter how many identities exist. Once you
accept that, the identity layer only has to be good enough to stop casual
abuse, and proof-of-work is good enough for that.

**What this gives up, honestly:** a determined attacker can burn the
day's budget in an hour and honest users get "come back tomorrow". We are
choosing *bounded loss* over *guaranteed availability*, because we can
survive a bad day and cannot survive a bad invoice.

### Traps checked

- **Trap in 5:** the breaker only works if metering is airtight. A request
  path that returns without being charged is a hole straight to the
  budget. → Every proxied request is charged, and a request whose usage
  cannot be parsed is charged a *pessimistic estimate*, never zero.
- **Trap in 2:** PoW difficulty tuned for a laptop is nothing to a server.
  → Difficulty escalates per address bucket: the fourth mint from one
  address today costs 4x the first, the eighth costs 1024x. No ASN
  database, no blocklist, self-throttling. Escalation prices the
  challenge being *issued*, not the redemption — counted at redemption,
  a batch of cheap challenges collected up front would bypass the whole
  ladder — and each challenge is signed to the bucket it was issued to.
- **Trap in the proxy:** metering happens *after* the model runs, so a
  single admitted request could overshoot. → The worst case a request
  could cost is *reserved* against both budgets at admission and
  reconciled to the real charge when it settles, so the budgets are
  ceilings, not horizons. Clamped `max_tokens` and request size keep the
  worst case small enough that reservations do not choke concurrency.
- **Trap in "no signup":** users may not realise their prompts transit a
  third party. → `deepseek free` prints exactly what leaves the machine
  and where it goes, before it enrols. Consent is the act of running it.
- **Trap everywhere:** we become the party responsible for what strangers
  generate. → Every upstream request carries DeepSeek's own `user_id`
  (see below), which is the mechanism they built for this, and tokens are
  revocable by subject.

---

## Architecture

```
  deepseek CLI  ─┐
                 ├─→  dsgate  ──(our real key + user_id)──→  api.deepseek.com
  playground    ─┘     │
   (browser)           └── journal.jsonl  (counts only, never prompts)
```

`dsgate` is a **policy-enforcing transparent proxy**, not a reimplementation
of the API. That choice is load-bearing:

The CLI already talks to one configurable base URL in four wire formats.
If the gateway proxies faithfully, then pointing at it makes `chat`,
`anthropic`, `respond`, `fim` and `models` all work with no client
changes — and so does anything DeepSeek ships next month. A typed
per-endpoint gateway would have to be extended for each one and would
drift from upstream the day it shipped.

So the gateway's job is narrow: authenticate, apply policy to the request,
pass the bytes, meter the response, charge the account.

### `user_id` is not optional

From `quick_start/rate_limit`, DeepSeek's own documentation:

> `user_id` is used to distinguish user identities on your business side
> … Content Safety Isolation … KVCache Isolation … Scheduling Isolation

This is the mechanism DeepSeek built for exactly what we are doing —
one account fronting many end users. The gateway sets `user_id` to the
token's subject on every upstream request, which buys three things:

- **Safety attribution.** One user generating violating content is
  attributed to that user, not to the whole account.
- **KV cache isolation.** Without it, users would share a prompt cache.
  Two strangers' prompts in one cache namespace is a privacy leak, and it
  would be *our* bug.
- **Scheduling isolation.** Per-user concurrency instead of one shared
  pool.

The gateway **overrides** any client-supplied `user_id` rather than
honouring it. A client that could choose its own `user_id` could poison
another subject's safety record or aim at their cache namespace.

### Metering

Measured on 2026-08-05 against the live API: **all three streaming
formats emit usage in their final event, whether or not you ask for it.**

| format | where usage arrives |
|---|---|
| OpenAI chat | last `data:` chunk before `[DONE]`, `usage` object |
| Anthropic | `message_delta` event, `usage` object |
| Responses | `response.completed` event, `usage` object |

`stream_options` is documented as *not supported* on Responses, and the
chat endpoint sends usage without it. So the gateway never has to rewrite
a streaming request to be able to bill it — it tees the stream, passes
every byte through untouched, and parses the tail. That removes the one
compromise this design would otherwise have had.

If parsing fails anyway, the request is charged its full reservation —
one input token per body byte plus `max_tokens` and a chain-of-thought
allowance of output, priced deliberately above anything it could have
cost. Unbillable must never mean free. The same figure is what Admit
reserves, which is why the two cannot diverge.

The journal is the budget's memory, so it fails closed: if a debit
cannot be written and fsync'd, the gateway stops admitting requests
until it can (and retries the journal on each refused admit, so a
recovered disk heals without a restart).

### State

No database. In-memory counters plus an append-only `journal.jsonl`,
fsync'd per debit, rotated daily, replayed at boot. Same shape as the
CLI's own usage ledger, for the same reason: you can read it with `tail`.

The volumes justify it — hundreds of requests a day, not thousands a
second — and it keeps the gateway a **single static binary with zero
dependencies**, which is what earns it a place on a 1 GiB box that is
already someone else's production.

The journal records subject, timestamp, endpoint, token counts and cost.
**It never records prompts, completions, IP addresses or headers.** That
is a promise made in `deepseek free`'s disclosure text, so it is a
promise the code has to keep.

### The token

```
dsf_<base64url(payload)>.<base64url(hmac-sha256(payload, secret))>
payload = version:1 | tier:1 | issued:4 | subject:16      (22 bytes)
```

Verification is one HMAC — stateless, no lookup, no user table. The
subject doubles as the DeepSeek `user_id` (base64url of 16 random bytes
is already inside their `[a-zA-Z0-9\-_]+` rule). Quota counters are keyed
by subject and expire at UTC midnight.

Tokens themselves expire after `DSGATE_TOKEN_TTL_DAYS` (7 by default),
enforced against the signed `issued` timestamp — otherwise identities
minted cheaply over months could be stockpiled and spent together. The
CLI renews its enrolment quietly at day 6, so an honest user never sees
the expiry.

### The mint

1. `POST /v1/anon/challenge` → an HMAC-signed challenge carrying its own
   difficulty and expiry. Stateless; nothing stored until it is redeemed.
2. Client finds a nonce where `sha256(challenge:nonce)` has `difficulty`
   leading zero bits.
3. `POST /v1/anon/token` → verified, marked single-use, token issued.

Difficulty is chosen server-side from how many challenges that address
bucket has already been *issued* today — counted at issuance and
persisted across restarts, so neither batching challenges nor a deploy
resets the ladder — and signed into the challenge along with a hash of
the issuing bucket, so it can only be redeemed from where it was asked
for. The first mint of the day is about a second of one core; the eighth
is a quarter of an hour.

Minting buckets IPv4 per address and IPv6 per **/48** — the block a site
is delegated. Bucketing IPv6 per /64 would let anyone with an ordinary
home allocation mint from 65,536 "different" addresses. Per-request rate
limiting uses the looser /64, where one bucket is one LAN; minting is the
scarce operation and gets the conservative boundary.

---

## Limits, and why these numbers

Worst-case cost per anonymous user per day, at flash rates
($0.14/M input, $0.28/M output):

```
60,000 input  × $0.14/M = $0.0084
20,000 output × $0.28/M = $0.0056
                          -------
                          $0.014 / user / day  at full burn
```

So a $1/day budget serves ~70 users burning *everything*, or several
hundred normal ones — a normal turn is a few hundred input and a few
hundred output tokens, about $0.0003. Roughly **3,300 ordinary turns per
dollar.**

| setting | default | why |
|---|---|---|
| `DSGATE_ANON_DAILY_REQUESTS` | 30 | enough to be useful for a day's work, not enough to script against |
| `DSGATE_ANON_DAILY_INPUT_TOKENS` | 60000 | ~150 pages of context per day |
| `DSGATE_ANON_DAILY_OUTPUT_TOKENS` | 20000 | the expensive side; the real cap |
| `DSGATE_ANON_MAX_TOKENS` | 4096 | clamps a single response, bounding overshoot |
| `DSGATE_MAX_BODY_BYTES` | 131072 | ~32K tokens; bounds the input side of overshoot |
| `DSGATE_DAILY_BUDGET_USD` | 1.00 | the circuit breaker; the number that actually protects us |
| `DSGATE_TOTAL_BUDGET_USD` | 20.00 | the credit pool; when it empties, the service says so |
| `DSGATE_MINT_DAILY_PER_IP` | 3 | beyond this, difficulty escalates rather than refusing |
| `DSGATE_POW_BITS` | 20 | ~1s on one core; 22 if we get farmed |
| `DSGATE_MAX_INFLIGHT` | 8 | protects a 1 GiB box; spend is bounded by reservations |
| `DSGATE_SUBJECT_REQUESTS_PER_MINUTE` | 6 | one token cannot dominate the minute |
| `DSGATE_SUBJECT_INFLIGHT` | 2 | one token cannot park the whole service |
| `DSGATE_TOKEN_TTL_DAYS` | 7 | identities age out instead of accumulating |
| `DSGATE_BALANCE_CHECK_MINUTES` | 15 | the ledger's "we have credit" is checked against the real account |

**Free tier is flash only.** Pro is 3x the price and the request is
*rejected*, not silently downgraded — a user who asked for pro and got
flash without being told would draw wrong conclusions and blame the model.

---

## What we deliberately did not build

- **A user table.** Stateless tokens plus daily counters. Nothing to
  breach, nothing to migrate, nothing to GDPR.
- **ASN / datacenter-IP blocking.** Needs a database we would have to
  keep current. Escalating difficulty gets most of the benefit for none
  of the upkeep.
- **Per-request proof-of-work.** It would add a second of latency to
  every call to defend against something rate limits already handle.
- **Scraping status.deepseek.com.** Same ruling as `deepseek status`: a
  parser against markup nobody promised would eventually report "all
  systems operational" forever.
- **Prompt logging.** Not for debugging, not behind a flag. A flag that
  can log prompts is a flag that will.
