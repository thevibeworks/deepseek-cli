# Running dsgate

The free-tier gateway is one static binary with no dependencies and no
database. It wants about 20 MB of RAM, a writable directory, and a real
DeepSeek API key. Everything else is a default.

Read [DESIGN.md](../DESIGN.md) first if you are changing the limits —
the numbers in there were derived, not picked.

## The smallest thing that works

```bash
DSGATE_UPSTREAM_KEY=sk-... dsgate
```

That listens on `:8787`, writes state to `./state`, serves
`deepseek-v4-flash`, and stops spending at **$1.00 a day / $20.00
total**. Point a CLI at it:

```bash
DEEPSEEK_FREE_URL=http://localhost:8787 deepseek free
DEEPSEEK_FREE_URL=http://localhost:8787 deepseek chat "hello"
```

`dsgate --help` prints every setting and its default.

## Spending less of it

```bash
DSGATE_UPSTREAM_KEY=sk-... OPENCODE_API_KEY=... dsgate
```

With a [Zen](https://opencode.ai/docs/zen) key, chat requests go to its
free DeepSeek model first and cost nothing; what it refuses — roughly one
request in five — falls back to the DeepSeek key before the caller sees
anything. `GET /v1/status` reports the share it is carrying under
`free_lane`, and the log line at boot says whether it is on at all.

Two things to know before turning it on. Zen's free lane says the prompts
it sees **may be used to improve the model**, which is a different promise
from the one the paid path makes, so say so wherever you tell users where
their prompts go. And it only carries `chat`: FIM, the Anthropic and
Responses formats, and web search still spend real credit.

## Docker

```bash
docker run -d --name dsgate \
  -p 127.0.0.1:8787:8787 \
  -v dsgate-state:/var/lib/dsgate \
  -e DSGATE_UPSTREAM_KEY=sk-... \
  -e DSGATE_TRUST_PROXY=1 \
  -e DSGATE_ANNOUNCE=https://free.example.com \
  ghcr.io/thevibeworks/dsgate:latest
```

Or `docker compose up -d` with the compose file here, after copying
`.env.example` to `.env`.

## Behind a reverse proxy

**`DSGATE_TRUST_PROXY=1` is required behind one and dangerous without
one.** It makes `X-Forwarded-For` authoritative for identity and rate
limiting. Facing the internet directly, that header is written by whoever
is calling, and anyone who can set a header gets an unlimited supply of
fresh identities.

So: bind to loopback, terminate TLS in front, and set the flag. Caddy:

```caddyfile
free.example.com {
	reverse_proxy 127.0.0.1:8787 {
		# OVERWRITE the forwarded address. Caddy's default is to APPEND
		# to whatever X-Forwarded-For the client sent — and with
		# TRUST_PROXY=1 the gateway reads the leftmost entry, which
		# would be the client's own invention. One line closes it.
		header_up X-Forwarded-For {client_ip}

		# The API holds a connection for up to ten minutes before
		# inference starts. A shorter timeout here turns a normal slow
		# start into a failed request.
		transport http {
			read_timeout 15m
			write_timeout 15m
		}
		flush_interval -1
	}
}
```

`header_up X-Forwarded-For {client_ip}` is not optional: without it,
anyone who can set a header mints identities from addresses of their
choosing. `flush_interval -1` is not optional either — without it Caddy
buffers the response and every streamed answer arrives in one lump at
the end.

nginx wants `proxy_set_header X-Forwarded-For $remote_addr;` (overwrite,
never append), `proxy_buffering off;` and `proxy_read_timeout 900s;` for
the same reasons. It also inherits an access log by default —
`access_log off;` in the vhost, or the layer in front of the gateway
breaks the gateway's own "no IPs logged" promise.

## What has to be backed up

`$DSGATE_STATE_DIR` — and only for two reasons, both of which matter:

| file | losing it means |
|---|---|
| `secret` | every outstanding token stops verifying; every user has to run `deepseek free` again |
| `ledger/state.json` | lifetime spend resets to zero, and the credit pool refills itself |
| `ledger/journal-*.jsonl` | today's per-user counters reset; the day's spend is forgotten |
| `revoked.txt` | revoked tokens work again |

None of it is large — the journal is a line per request — and none of it
contains a prompt.

## Day to day

```bash
# Is it up, and how much has it spent?
curl -s -H "X-Admin-Token: $DSGATE_ADMIN_TOKEN" localhost:8787/admin/health

# What did it spend it on? One line per request, no prompts.
tail -f state/ledger/journal-$(date -u +%F).jsonl

# Cut off one subject without restarting (a restart drops every stream
# in flight; this does not).
echo "aBcDeFgHiJkLmNoPqRsTuV" >> state/revoked.txt
docker kill -s HUP dsgate
```

The subject to revoke is in the journal line, and in the `user_id` of the
upstream request if DeepSeek is the one complaining.

## Raising or lowering the limits

The two that actually bound the spend:

```bash
DSGATE_DAILY_BUDGET_USD=1.00    # circuit breaker, resets 00:00 UTC
DSGATE_TOTAL_BUDGET_USD=20.00   # the credit pool
```

Everything else is fairness between users, not protection. If you are
being farmed, the lever is `DSGATE_POW_BITS` (each +1 doubles the cost of
minting an identity) and `DSGATE_MINT_DAILY_PER_IP` (below which
difficulty escalates per address). Raising the per-user quotas does not
put the budget at risk; the breaker still holds.

## When it runs out

Both budgets return a 402 with a message pointing at
`platform.deepseek.com/api_keys`. Nothing else changes — the service
stays up, `deepseek free status` still works, and it starts serving again
the moment `DSGATE_TOTAL_BUDGET_USD` is raised past what has been spent.
The daily budget clears on its own at 00:00 UTC.
