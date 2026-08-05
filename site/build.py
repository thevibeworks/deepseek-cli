#!/usr/bin/env python3
"""Build the deepseek-cli site.

Six pages that share a head, a header and a footer. Writing those by hand
six times is how a canonical URL ends up pointing at the wrong page and
nobody notices for a year, so the shared parts live here once and the
pages carry only their own content.

    python3 site/build.py           # write the HTML
    python3 site/build.py --check   # fail if the committed HTML is stale

Stdlib only, no build step for anyone who just wants to read the site.
"""

from __future__ import annotations

import argparse
import html
import pathlib
import sys

SITE = "https://thevibeworks.github.io/deepseek-cli"
REPO = "https://github.com/thevibeworks/deepseek-cli"
DOCS = "https://api-docs.deepseek.com"
OG_IMAGE = f"{SITE}/og.png"
ROOT = pathlib.Path(__file__).resolve().parent

# Every page, in reading order. The order drives the nav, the pager links
# and the sitemap, so there is exactly one list to keep correct.
NAV = [
    ("", "overview"),
    ("install/", "install"),
    ("commands/", "commands"),
    ("formats/", "formats"),
    ("cost/", "cost"),
    ("agents/", "agents"),
]


def head(*, slug, title, description, keywords, jsonld, crumb_title):
    """The <head> for one page: canonical, social cards, structured data."""
    url = f"{SITE}/{slug}" if slug else f"{SITE}/"
    depth = slug.count("/") if slug else 0
    root = "../" * depth if depth else ""

    breadcrumb = {
        "@context": "https://schema.org",
        "@type": "BreadcrumbList",
        "itemListElement": [
            f'{{"@type":"ListItem","position":1,"name":"deepseek-cli","item":"{SITE}/"}}'
        ],
    }
    crumbs = [
        '{"@type":"ListItem","position":1,"name":"deepseek-cli","item":"%s/"}' % SITE
    ]
    if slug:
        crumbs.append(
            '{"@type":"ListItem","position":2,"name":"%s","item":"%s"}'
            % (html.escape(crumb_title), url)
        )

    return f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(title)}</title>
<meta name="description" content="{html.escape(description)}">
<meta name="keywords" content="{html.escape(keywords)}">
<meta name="author" content="thevibeworks">
<meta name="theme-color" content="#000000">
<link rel="canonical" href="{url}">
<meta property="og:type" content="website">
<meta property="og:site_name" content="deepseek-cli">
<meta property="og:title" content="{html.escape(title)}">
<meta property="og:description" content="{html.escape(description)}">
<meta property="og:url" content="{url}">
<meta property="og:image" content="{OG_IMAGE}">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta property="og:image:alt" content="deepseek-cli — the whole DeepSeek API from the terminal">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="{html.escape(title)}">
<meta name="twitter:description" content="{html.escape(description)}">
<meta name="twitter:image" content="{OG_IMAGE}">
<link rel="icon" href="{root}favicon.svg" type="image/svg+xml">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="{root}style.css">
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@300;400;700&display=swap" rel="stylesheet">
<script type="application/ld+json">
{jsonld}
</script>
<script type="application/ld+json">
{{"@context":"https://schema.org","@type":"BreadcrumbList","itemListElement":[{",".join(crumbs)}]}}
</script>
</head>
<body>
<a class="skip" href="#main">skip to content</a>
<header class="masthead">
  <div class="wrap">
    <a class="brand" href="{root or './'}">
      <span class="caret">&gt;</span><span class="org">thevibeworks/</span><span class="name">deepseek-cli</span>
    </a>
    <nav class="sitenav" aria-label="Sections">
{nav_links(slug, root)}
      <a href="{REPO}">github&nbsp;&#8599;</a>
    </nav>
  </div>
</header>
<main id="main">
  <div class="wrap">
"""


def nav_links(current, root):
    out = []
    for slug, label in NAV:
        href = f"{root}{slug}" if slug else (root or "./")
        cur = ' aria-current="page"' if slug == current else ""
        out.append(f'      <a href="{href}"{cur}>{label}</a>')
    return "\n".join(out)


def crumb(title, root):
    if not title:
        return ""
    return (
        f'<nav class="crumb" aria-label="Breadcrumb">'
        f'<a href="{root or "./"}">deepseek-cli</a>'
        f'<span class="sep">/</span>{html.escape(title)}</nav>\n'
    )


def pager(slug, root):
    """Previous/next links. Sequential reading beats a wall of nav."""
    idx = [s for s, _ in NAV].index(slug)
    parts = ['<nav class="pager" aria-label="Pagination">']
    if idx > 0:
        s, label = NAV[idx - 1]
        href = f"{root}{s}" if s else (root or "./")
        parts.append(f'<a href="{href}"><span class="lbl">&larr; previous</span>{label}</a>')
    else:
        parts.append("<span></span>")
    if idx < len(NAV) - 1:
        s, label = NAV[idx + 1]
        parts.append(
            f'<a href="{root}{s}" style="text-align:right"><span class="lbl">next &rarr;</span>{label}</a>'
        )
    parts.append("</nav>")
    return "\n".join(parts)


FOOT = """  </div>
</main>
<footer class="foot">
  <div class="wrap">
    <span>
      <a href="{repo}">source</a> &middot;
      <a href="{repo}/blob/main/LICENSE">MIT</a> &middot;
      <a href="{site}/llms.txt">llms.txt</a> &middot;
      <a href="https://thevibeworks.github.io/">thevibeworks</a>
    </span>
    <span>unofficial &mdash; not affiliated with DeepSeek</span>
  </div>
</footer>
</body>
</html>
"""


def render(page):
    slug = page["slug"]
    depth = slug.count("/") if slug else 0
    root = "../" * depth if depth else ""
    body = head(
        slug=slug,
        title=page["title"],
        description=page["description"],
        keywords=page["keywords"],
        jsonld=page["jsonld"],
        crumb_title=page.get("crumb", ""),
    )
    body += crumb(page.get("crumb", ""), root)
    body += page["body"].replace("{{root}}", root or "./").replace("{{repo}}", REPO).replace("{{docs}}", DOCS)
    body += pager(slug, root)
    body += FOOT.format(repo=REPO, site=SITE)
    return body


SOFTWARE_JSONLD = """{
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  "name": "deepseek-cli",
  "alternateName": ["deepseek", "ds", "dscli"],
  "applicationCategory": "DeveloperApplication",
  "operatingSystem": "macOS, Linux, Windows",
  "description": "Command-line client for the whole DeepSeek API: chat completions in OpenAI, Anthropic Messages and OpenAI Responses formats, FIM completion, models and balance, with per-call cost and context-cache accounting.",
  "url": "https://thevibeworks.github.io/deepseek-cli/",
  "downloadUrl": "https://github.com/thevibeworks/deepseek-cli/releases",
  "codeRepository": "https://github.com/thevibeworks/deepseek-cli",
  "programmingLanguage": "Go",
  "license": "https://opensource.org/licenses/MIT",
  "isAccessibleForFree": true,
  "offers": {"@type": "Offer", "price": "0", "priceCurrency": "USD"},
  "author": {"@type": "Organization", "name": "thevibeworks", "url": "https://github.com/thevibeworks"}
}"""


def tech_article(name, description, slug):
    return f"""{{
  "@context": "https://schema.org",
  "@type": "TechArticle",
  "headline": "{name}",
  "description": "{description}",
  "url": "{SITE}/{slug}",
  "isPartOf": {{"@type": "WebSite", "name": "deepseek-cli", "url": "{SITE}/"}},
  "author": {{"@type": "Organization", "name": "thevibeworks"}},
  "inLanguage": "en"
}}"""


def faq(pairs):
    items = ",".join(
        '{"@type":"Question","name":%s,"acceptedAnswer":{"@type":"Answer","text":%s}}'
        % (jstr(q), jstr(a))
        for q, a in pairs
    )
    return '{"@context":"https://schema.org","@type":"FAQPage","mainEntity":[%s]}' % items


def jstr(s):
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


# --------------------------------------------------------------------------
# Pages
# --------------------------------------------------------------------------

PAGES = []

PAGES.append(dict(
    slug="",
    title="deepseek-cli — the whole DeepSeek API from the terminal",
    description="A single Go binary for every DeepSeek API: chat completions in OpenAI, Anthropic and Responses formats, FIM, models and balance — with multi-turn that survives the reasoning round-trip and per-call cost accounting.",
    keywords="deepseek cli, deepseek api, deepseek command line, deepseek-v4-flash, deepseek-v4-pro, deepseek anthropic api, deepseek responses api, deepseek context cache, deepseek pricing, llm cli",
    jsonld=SOFTWARE_JSONLD,
    body="""
<section class="hero">
<h1><span class="caret">&gt;</span> deepseek-cli</h1>
<p class="lede">DeepSeek serves the same two models through four different wire
formats. Every other client picks one. This speaks all four &mdash; from one
binary, with the multi-turn bookkeeping kept straight and a running tally of
what each call cost.</p>

<p class="badges">
<img src="https://img.shields.io/github/v/release/thevibeworks/deepseek-cli?color=00c2e9&labelColor=0d0d0d&label=release" alt="Latest release" width="104" height="20" loading="lazy">
<img src="https://img.shields.io/badge/API%20coverage-6%2F6%20endpoints-00ff41?labelColor=0d0d0d" alt="API coverage: 6 of 6 endpoints" width="188" height="20" loading="lazy">
<img src="https://img.shields.io/badge/tests-102%20%C2%B7%2068%25%20covered-00ff41?labelColor=0d0d0d" alt="102 tests, 68 percent covered" width="166" height="20" loading="lazy">
<img src="https://img.shields.io/badge/DeepSeek%20API%20docs-2026--08--02-bf00ff?labelColor=0d0d0d" alt="Built against the DeepSeek API docs of 2026-08-02" width="196" height="20" loading="lazy">
<img src="https://img.shields.io/badge/models-v4--flash%20%7C%20v4--pro-00c2e9?labelColor=0d0d0d" alt="Models: deepseek-v4-flash and deepseek-v4-pro" width="164" height="20" loading="lazy">
</p>

<div class="term">
<div class="term-bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span><span class="title">~/work</span></div>
<pre><code><span class="p">$</span> ds chat <span class="w">"explain this diff"</span> <span class="k">--file</span> changes.patch
<span class="o">The patch swaps the retry loop for exponential backoff, and stops
retrying 4xx responses &mdash; those would fail identically on a second try.</span>
<span class="c">&middot; flash &middot; 3.2k in (87% cached) &middot; 412 out (180 think) &middot; ~$0.000178 &middot; 2.1s</span></code></pre>
</div>

<div class="cta">
<a class="btn" href="{{root}}install/">install</a>
<a class="btn alt" href="{{root}}commands/">commands</a>
<a class="btn alt" href="{{repo}}">source</a>
</div>
</section>

<h2 id="why">Why this exists</h2>
<p>Wrapping six HTTP endpoints is not interesting on its own. Two things are,
and they are the reason this is not a shell function around <code>curl</code>.</p>

<ul class="grid">
<li class="card">
<h3>Multi-turn that does not 400</h3>
<p>With tools in play, DeepSeek rejects any request that fails to replay every
assistant <code>reasoning_content</code>. Without tools it ignores the same
field &mdash; so sending it just burns input tokens. <strong>Sessions get both
halves right</strong>, and you never think about it.</p>
</li>
<li class="card">
<h3>Cost you can actually see</h3>
<p>A cached input token is <strong>50&times; cheaper</strong> than an uncached
one. That split is invisible unless something reads
<code>prompt_cache_hit_tokens</code> and does the arithmetic. This does, on
every call, and keeps a <a href="{{root}}cost/">local ledger</a>.</p>
</li>
<li class="card">
<h3>Four formats, one binary</h3>
<p>Send the same prompt through the OpenAI, Anthropic and Responses formats and
<a href="{{root}}formats/">see what differs</a>. Useful when you are pointing
Claude Code or Codex at DeepSeek and something behaves oddly.</p>
</li>
<li class="card">
<h3>Built for scripts and agents</h3>
<p>stdout is data, stderr is status, <code>--json</code> is the API's own
response body unwrapped. Exit codes separate <em>bad key</em> from <em>no
balance</em> from <em>rate limited</em>. <a href="{{root}}agents/">The
contract</a>.</p>
</li>
</ul>

<h2 id="quickstart">Quick start</h2>
<pre><code>curl -sL {{repo}}/raw/main/install.sh | sh

export DEEPSEEK_API_KEY=sk-...
ds check                      # is everything reachable?
ds chat "why is the sky blue"</code></pre>

<p><code>check</code> calls all six endpoints once and reports which answered.
It is the first thing to run when something is wrong and you do not yet know
whether the problem is the key, the balance, the network, a proxy in between,
or one specific endpoint.</p>

<div class="term">
<div class="term-bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span><span class="title">ds check</span></div>
<pre><code><span class="c">https://api.deepseek.com</span>

<span class="p">ok</span>    GET /models                  141ms  deepseek-v4-flash, deepseek-v4-pro
<span class="p">ok</span>    GET /user/balance            126ms  18.48 CNY
<span class="p">ok</span>    POST /chat/completions       651ms  5 in / 1 out
<span class="p">ok</span>    POST /anthropic/v1/messages  590ms  5 in / 1 out
<span class="p">ok</span>    POST /responses              595ms  5 in / 9 out
<span class="p">ok</span>    POST /beta/completions       379ms  4 in / 1 out

<span class="c">all endpoints reachable</span></code></pre>
</div>

<h2 id="commands">The commands</h2>
<p>One per endpoint, named for what it does rather than for its path.
Full reference on the <a href="{{root}}commands/">commands page</a>.</p>

<div class="tablewrap">
<table>
<thead><tr><th>Command</th><th>Endpoint</th><th>What it is for</th></tr></thead>
<tbody>
<tr><td><code>chat</code></td><td><code>POST /chat/completions</code></td><td>The default. OpenAI format, the one most tools speak.</td></tr>
<tr><td><code>anthropic</code></td><td><code>POST /anthropic/v1/messages</code></td><td>What Claude Code and the Anthropic SDKs speak.</td></tr>
<tr><td><code>respond</code></td><td><code>POST /responses</code></td><td>What Codex speaks. JSON Schema output and server-side web search live only here.</td></tr>
<tr><td><code>fim</code></td><td><code>POST /beta/completions</code></td><td>Fill in the middle &mdash; the shape editors use for inline completion.</td></tr>
<tr><td><code>models</code></td><td><code>GET /models</code></td><td>Available models, joined with the published rate card.</td></tr>
<tr><td><code>balance</code></td><td><code>GET /user/balance</code></td><td>What is left, per currency.</td></tr>
<tr><td><code>usage</code></td><td><em>local</em></td><td>What this CLI has spent, from its own ledger.</td></tr>
<tr><td><code>session</code></td><td><em>local</em></td><td>The conversations <code>chat --continue</code> replays.</td></tr>
<tr><td><code>check</code></td><td><em>all six</em></td><td>Preflight.</td></tr>
<tr><td><code>raw</code></td><td><em>anything</em></td><td>Escape hatch &mdash; any path, with auth and retries.</td></tr>
</tbody>
</table>
</div>

<h2 id="honest">What it does not do</h2>
<p>Worth knowing before you install it:</p>
<ul>
<li><strong>It does not run tool calls.</strong> It prints the calls a model
wants to make, which is what you need to develop a tool schema. Executing
model-chosen commands is an agent runtime and a much larger set of safety
questions.</li>
<li><strong>It is not a coding agent.</strong> No file editing, no repo
awareness, no loop. It sends a request and shows you the response.</li>
<li><strong>Costs are estimates.</strong> Computed from DeepSeek's published
USD rate card, not from your invoice. Token counts are exact, and they are what
gets stored, so old calls can be repriced when the card changes.</li>
<li><strong>Text only.</strong> DeepSeek rejects image, document and
search-result content blocks in every format &mdash; that is the API, not a
gap here.</li>
</ul>
""",
))

PAGES.append(dict(
    slug="install/",
    crumb="install",
    title="Install deepseek-cli — binaries, Go, and the ds alias",
    description="Install the deepseek CLI on macOS, Linux or Windows: one-line installer, release binaries, or go install. Covers the ds and dscli aliases, API key configuration, and shell completion.",
    keywords="install deepseek cli, deepseek cli download, deepseek cli macos, deepseek cli linux, go install deepseek, deepseek api key setup, ds alias",
    jsonld=tech_article("Install deepseek-cli", "How to install the deepseek CLI and configure an API key.", "install/"),
    body="""
<h1>Install</h1>
<p class="lede">A single static binary with no runtime dependencies. Pick
whichever of these you trust most.</p>

<h2 id="script">One line</h2>
<p>Detects your platform, downloads the matching release, installs the binary
and both aliases:</p>
<pre><code>curl -sL {{repo}}/raw/main/install.sh | sh</code></pre>
<p class="small">Installs to <code>/usr/local/bin</code>; set
<code>PREFIX=$HOME/.local</code> to install without <code>sudo</code>. If you
would rather read it first &mdash; and you should, for anything piped to a
shell &mdash; it is <a href="{{repo}}/blob/main/install.sh">forty lines</a>.</p>

<h2 id="binaries">Release binaries</h2>
<p>From <a href="{{repo}}/releases">Releases</a>, for macOS, Linux and Windows
on both amd64 and arm64:</p>
<pre><code># macOS (Apple Silicon)
curl -sL {{repo}}/releases/latest/download/deepseek_darwin_arm64.tar.gz | tar xz
sudo mv deepseek /usr/local/bin/

# Linux (x86_64)
curl -sL {{repo}}/releases/latest/download/deepseek_linux_amd64.tar.gz | tar xz
sudo mv deepseek /usr/local/bin/</code></pre>
<p class="small">Every release ships a <code>checksums.txt</code>.</p>

<h2 id="go">Go</h2>
<pre><code>go install github.com/thevibeworks/deepseek-cli/cmd/deepseek@latest</code></pre>
<p>Requires Go 1.25 or later. The binary lands in <code>$(go env GOPATH)/bin</code>.</p>

<h2 id="aliases">ds and dscli</h2>
<p><code>deepseek</code> is eight characters to type many times a day. The
binary answers to <code>ds</code> and <code>dscli</code> as well.</p>
<p>These are <strong>symlinks to the same binary</strong>, not shell aliases,
so they work in scripts, <code>cron</code>, <code>Makefile</code>s and anywhere
else a shell alias would not exist. The binary notices which name invoked it,
so <code>ds --help</code> says <code>ds</code>.</p>
<pre><code>ds chat "why is the sky blue"
ds usage --since 7d</code></pre>
<p>The installer and <code>make install</code> create them. By hand:</p>
<pre><code>sudo ln -sf deepseek /usr/local/bin/ds
sudo ln -sf deepseek /usr/local/bin/dscli</code></pre>

<h2 id="key">The API key</h2>
<p>Get one from <a href="https://platform.deepseek.com/api_keys">platform.deepseek.com</a>,
then either put it in the environment:</p>
<pre><code>export DEEPSEEK_API_KEY=sk-...</code></pre>
<p>or, to keep it out of the environment entirely, in a file:</p>
<pre><code>mkdir -p ~/.config/deepseek &amp;&amp; chmod 700 ~/.config/deepseek
printf 'sk-...' &gt; ~/.config/deepseek/api_key
chmod 600 ~/.config/deepseek/api_key</code></pre>
<p>Precedence is <code>--api-key</code>, then <code>$DEEPSEEK_API_KEY</code>,
then the file.</p>

<h2 id="config">Configuration</h2>
<div class="tablewrap">
<table>
<thead><tr><th>Variable</th><th>Purpose</th><th>Default</th></tr></thead>
<tbody>
<tr><td><code>DEEPSEEK_API_KEY</code></td><td>API key</td><td>&mdash;</td></tr>
<tr><td><code>DEEPSEEK_BASE_URL</code></td><td>Override the base URL for proxies and gateways</td><td><code>https://api.deepseek.com</code></td></tr>
<tr><td><code>DEEPSEEK_CONFIG_DIR</code></td><td>Where the key file lives</td><td><code>~/.config/deepseek</code></td></tr>
<tr><td><code>DEEPSEEK_STATE_DIR</code></td><td>Usage ledger and saved conversations</td><td><code>~/.local/state/deepseek</code></td></tr>
</tbody>
</table>
</div>
<p>Both directories respect <code>XDG_CONFIG_HOME</code> and
<code>XDG_STATE_HOME</code>.</p>

<div class="note warn">
<span class="tag">careful</span>
<p>Setting <code>--base-url</code> sends your API key and your prompts to that
host instead of DeepSeek. Only point it at a gateway you control.</p>
</div>

<h2 id="verify">Verify</h2>
<pre><code>ds check</code></pre>
<p>Calls all six endpoints once with a one-token cap &mdash; the whole check
costs a fraction of a cent. Exit code 2 means the key is missing or rejected,
3 means the balance is exhausted.</p>

<h2 id="completion">Shell completion</h2>
<pre><code>ds completion zsh  &gt; "${fpath[1]}/_deepseek"    # zsh
ds completion bash &gt; /etc/bash_completion.d/deepseek
ds completion fish &gt; ~/.config/fish/completions/deepseek.fish</code></pre>
""",
))

PAGES.append(dict(
    slug="commands/",
    crumb="commands",
    title="deepseek-cli command reference — chat, anthropic, respond, fim, usage",
    description="Full reference for every deepseek CLI command and flag: chat completions, Anthropic Messages, OpenAI Responses, FIM, models, balance, usage ledger, sessions, check and raw, plus exit codes.",
    keywords="deepseek cli commands, deepseek chat completions cli, deepseek fim completion, deepseek cli flags, deepseek cli reference, deepseek json output, deepseek cli exit codes",
    jsonld=tech_article("deepseek-cli command reference", "Every command and flag in the deepseek CLI.", "commands/"),
    body="""
<h1>Commands</h1>
<p class="lede">Ten commands: six wrap an endpoint, two read local state, one
is a preflight, one is an escape hatch. <code>--help</code> on any of them
carries the same detail.</p>

<h2 id="chat">chat</h2>
<p><code>POST /chat/completions</code> &mdash; the default door, and the only
format that supports chat prefix completion.</p>
<pre><code>ds chat "why is the sky blue"
git diff | ds chat "write a commit message"
ds chat "explain" --file server.go --file server_test.go
ds chat "review this" --model deepseek-v4-pro --effort max
ds chat "and now in one line" --continue</code></pre>
<p>Arguments are the instruction; piped stdin and <code>--file</code> are the
material. All three compose.</p>

<div class="tablewrap">
<table>
<thead><tr><th>Flag</th><th>Effect</th></tr></thead>
<tbody>
<tr><td><code>-m, --model</code></td><td><code>deepseek-v4-flash</code> (default) or <code>deepseek-v4-pro</code></td></tr>
<tr><td><code>-s, --system</code></td><td>System prompt, inline or <code>@file</code></td></tr>
<tr><td><code>--think on|off</code></td><td>Thinking mode. Default is the API's own, which is on</td></tr>
<tr><td><code>-e, --effort</code></td><td><code>low</code>, <code>high</code> or <code>max</code></td></tr>
<tr><td><code>--max-tokens</code></td><td>Cap generated tokens</td></tr>
<tr><td><code>--temperature</code>, <code>--top-p</code></td><td>Sampling. Both ignored in thinking mode</td></tr>
<tr><td><code>--stop</code></td><td>Stop sequence, repeatable, max 16</td></tr>
<tr><td><code>--response-format</code></td><td><code>text</code> or <code>json_object</code></td></tr>
<tr><td><code>--tool</code></td><td>Tool definition as JSON or <code>@file</code>, repeatable</td></tr>
<tr><td><code>--tool-choice</code></td><td><code>none</code>, <code>auto</code>, <code>required</code>, or a JSON object</td></tr>
<tr><td><code>--prefix</code></td><td>Beta: force the answer to start with this text</td></tr>
<tr><td><code>-f, --file</code></td><td>Attach a file's contents, repeatable</td></tr>
<tr><td><code>-c, --continue</code></td><td>Continue the conversation named <code>last</code></td></tr>
<tr><td><code>--session NAME</code></td><td>Read and write a named conversation</td></tr>
<tr><td><code>--stream=false</code></td><td>Wait for the whole answer</td></tr>
<tr><td><code>--reasoning=false</code></td><td>Hide the chain of thought</td></tr>
<tr><td><code>--logprobs</code>, <code>--top-logprobs</code></td><td>Token log probabilities</td></tr>
<tr><td><code>--user-id</code></td><td>Cache and scheduling isolation</td></tr>
</tbody>
</table>
</div>

<h3 id="multiturn">Multi-turn</h3>
<p>The API stores nothing, in any format, so conversations live on your
machine.</p>
<pre><code>ds chat "read this spec" --file spec.md --session review
ds chat "now list the risks"          --session review
ds session ls
ds session show review
ds session rm review</code></pre>
<p><code>--continue</code> is the session named <code>last</code>.</p>

<h3 id="tools">Tools</h3>
<pre><code>ds chat "weather in Hangzhou?" --tool @weather.json
# tool_call call_00_hUj... get_weather({"city": "Hangzhou"})</code></pre>
<p>One tool file works against every format &mdash; both the OpenAI
<code>parameters</code> and the Anthropic <code>input_schema</code> spellings
are accepted. The calls are printed, never executed.</p>

<h2 id="anthropic">anthropic</h2>
<p><code>POST /anthropic/v1/messages</code> &mdash; the format Claude Code, the
Anthropic SDKs and the Claude desktop app speak.</p>
<pre><code>ds anthropic "hello"
ds anthropic "hello" --model claude-opus-4-1 --json</code></pre>
<p>Claude model names are accepted and remapped server-side. The usage line
shows both names so the cost stays traceable to the model that actually ran.
See <a href="{{root}}formats/">formats</a> for the mapping.</p>

<h2 id="respond">respond</h2>
<p><code>POST /responses</code> &mdash; the format Codex speaks. Two things
live only here: JSON Schema structured output, and a <code>web_search</code>
tool DeepSeek runs server-side.</p>
<pre><code>ds respond "what shipped in Go 1.26" --web-search
ds respond "Berlin" -s "Return city and country." --schema @city.json</code></pre>
<p>Flash only, for now.</p>

<h2 id="fim">fim</h2>
<p><code>POST /beta/completions</code> &mdash; give it a prefix and an optional
suffix; it writes the middle.</p>
<pre><code>ds fim "def add(a, b):" --suffix "    return result"
ds fim --prefix @head.go --suffix @tail.go --max-tokens 200</code></pre>
<p>Beta, with two hard limits: output caps at 4K tokens, and it never thinks.</p>

<h2 id="models">models</h2>
<pre><code>ds models</code></pre>
<p>The API's model list joined with the published rate card, so the price is on
screen next to the model you are about to pick. <code>--json</code> returns the
API's list alone.</p>

<h2 id="balance">balance</h2>
<pre><code>ds balance</code></pre>
<p>Lists every currency the account holds &mdash; a real account returns both a
USD and a CNY row. Exits 3 when exhausted, the same code a 402 produces
anywhere else, so a script can check once up front.</p>

<h2 id="usage">usage</h2>
<pre><code>ds usage                  # today
ds usage --since 7d
ds usage --entries --json # individual calls</code></pre>
<p>Reports the local ledger. See <a href="{{root}}cost/">cost</a>.</p>

<h2 id="check">check</h2>
<pre><code>ds check</code></pre>
<p>Calls all six endpoints once. Every probe runs even after one fails,
because "all six rejected the key" is a different diagnosis from "only
<code>/responses</code> is unhappy".</p>

<h2 id="raw">raw</h2>
<pre><code>ds raw /models
ds raw /chat/completions --data @request.json
ds raw /anthropic/v1/messages --data @req.json --anthropic-auth</code></pre>
<p>Any path, with this CLI's auth, base URL, retries and error reporting.
Every other command is a typed convenience over this one, so an endpoint
DeepSeek ships tomorrow is reachable today.</p>

<h2 id="global">Global flags</h2>
<div class="tablewrap">
<table>
<thead><tr><th>Flag</th><th>Effect</th></tr></thead>
<tbody>
<tr><td><code>--json</code></td><td>Print the API's own response body, unwrapped</td></tr>
<tr><td><code>--jq EXPR</code></td><td>Filter that body through <code>jq</code></td></tr>
<tr><td><code>--api-key</code>, <code>--base-url</code></td><td>Override the resolved values</td></tr>
<tr><td><code>--timeout</code></td><td>Default 10m, matching how long the API may hold a connection before inference starts</td></tr>
<tr><td><code>-v</code>, <code>-vv</code></td><td>Log HTTP to stderr; <code>-vv</code> adds bodies</td></tr>
<tr><td><code>--no-stats</code></td><td>Suppress the token/cost line</td></tr>
<tr><td><code>--no-ledger</code></td><td>Do not record the call</td></tr>
</tbody>
</table>
</div>

<h2 id="exit">Exit codes</h2>
<div class="tablewrap">
<table>
<thead><tr><th class="num">Code</th><th>Meaning</th><th>What to do</th></tr></thead>
<tbody>
<tr><td class="num">0</td><td>success</td><td>&mdash;</td></tr>
<tr><td class="num">1</td><td>error</td><td>Read stderr</td></tr>
<tr><td class="num">2</td><td>auth</td><td>Key missing or rejected. Do not retry</td></tr>
<tr><td class="num">3</td><td>no balance</td><td>Top up. Do not retry</td></tr>
<tr><td class="num">4</td><td>rate limited</td><td>Back off, then retry</td></tr>
<tr><td class="num">130</td><td>interrupted</td><td>&mdash;</td></tr>
</tbody>
</table>
</div>
<p>Transport failures and 429/5xx are already retried internally with backoff,
so a non-zero exit means your own retry loop probably will not help either.</p>
""",
))

PAGES.append(dict(
    slug="formats/",
    crumb="formats",
    title="The four DeepSeek API formats, compared — deepseek-cli",
    description="DeepSeek exposes the same two models through four API formats. What differs between /chat/completions, /anthropic/v1/messages, /responses and FIM: auth, thinking controls, tools, and the token-accounting convention that trips up cost tracking.",
    keywords="deepseek anthropic api, deepseek openai compatible, deepseek responses api, deepseek claude code, deepseek codex, deepseek api format comparison, deepseek cache_read_input_tokens, deepseek model mapping",
    jsonld=faq([
        ("Which DeepSeek API format should I use?",
         "Use the OpenAI /chat/completions format unless you have a reason not to: it is the most widely supported and the only one with chat prefix completion. Use /anthropic/v1/messages when the surrounding tooling speaks Anthropic Messages, such as Claude Code. Use /responses when you need JSON Schema structured output or DeepSeek's server-side web search, or when the tool is Codex. Use FIM only for fill-in-the-middle code completion."),
        ("Does DeepSeek's Anthropic endpoint count tokens the same way?",
         "No. On /anthropic/v1/messages the usage.input_tokens field excludes cache reads, so the full prompt is input_tokens plus cache_read_input_tokens. The OpenAI chat and Responses formats use the opposite convention, where the input count already includes cached tokens. Treating them the same misprices every cached call."),
        ("What happens if I send a Claude model name to DeepSeek?",
         "It is remapped server-side. Model names starting with claude-opus map to deepseek-v4-pro; claude-sonnet and claude-haiku map to deepseek-v4-flash; anything unrecognised also falls back to deepseek-v4-flash."),
        ("Can DeepSeek accept images?",
         "No. DeepSeek is text only. Image, document and search-result content blocks are rejected or replaced with placeholder text in every format."),
    ]),
    body="""
<h1>Four wire formats</h1>
<p class="lede">DeepSeek exposes the same two models &mdash;
<code>deepseek-v4-flash</code> and <code>deepseek-v4-pro</code> &mdash; through
four different request shapes, so that existing ecosystems can point at it
without code changes. They are not interchangeable.</p>

<h2 id="which">Which one to use</h2>
<div class="tablewrap">
<table>
<thead><tr><th>Format</th><th>Path</th><th>Reach for it when</th></tr></thead>
<tbody>
<tr><td><strong>OpenAI chat</strong><br><code>ds chat</code></td><td><code>/chat/completions</code></td><td>Default. Widest tool support, and the only format with chat prefix completion.</td></tr>
<tr><td><strong>Anthropic Messages</strong><br><code>ds anthropic</code></td><td><code>/anthropic/v1/messages</code></td><td>The surrounding tooling speaks Anthropic &mdash; Claude Code, the Anthropic SDKs, the Claude desktop app.</td></tr>
<tr><td><strong>OpenAI Responses</strong><br><code>ds respond</code></td><td><code>/responses</code></td><td>You need JSON Schema output or server-side web search. Also what Codex speaks.</td></tr>
<tr><td><strong>FIM</strong><br><code>ds fim</code></td><td><code>/beta/completions</code></td><td>Fill-in-the-middle code completion. No chat structure at all.</td></tr>
</tbody>
</table>
</div>

<h2 id="differences">What actually differs</h2>
<div class="tablewrap">
<table>
<thead><tr><th></th><th>chat</th><th>anthropic</th><th>respond</th><th>fim</th></tr></thead>
<tbody>
<tr><th>Auth header</th><td>Bearer</td><td><code>x-api-key</code></td><td>Bearer</td><td>Bearer</td></tr>
<tr><th><code>max_tokens</code></th><td>optional</td><td><strong>required</strong></td><td>optional</td><td>optional</td></tr>
<tr><th>Thinking toggle</th><td><code>thinking.type</code></td><td><code>thinking.type</code></td><td><code>reasoning.effort: none</code></td><td>never thinks</td></tr>
<tr><th>Effort control</th><td><code>reasoning_effort</code></td><td><code>output_config.effort</code></td><td><code>reasoning.effort</code></td><td>&mdash;</td></tr>
<tr><th>JSON Schema output</th><td>&mdash;</td><td>&mdash;</td><td><strong>yes</strong></td><td>&mdash;</td></tr>
<tr><th>Server-side web search</th><td>&mdash;</td><td>&mdash;</td><td><strong>yes</strong></td><td>&mdash;</td></tr>
<tr><th>Prefix completion</th><td><strong>yes</strong> (beta path)</td><td>&mdash;</td><td>&mdash;</td><td>&mdash;</td></tr>
<tr><th>Models</th><td>both</td><td>both</td><td>flash only</td><td>both</td></tr>
<tr><th>Stream terminator</th><td><code>data: [DONE]</code></td><td><code>message_stop</code></td><td><code>response.completed</code></td><td><code>data: [DONE]</code></td></tr>
</tbody>
</table>
</div>

<h2 id="tokens">The token-accounting trap</h2>
<p>This is the one that silently corrupts cost tracking, and it is not stated
in the published docs. The formats disagree about what
<code>input_tokens</code> means.</p>

<div class="note">
<span class="tag">verified against the live API</span>
<p>The same prompt, sent both ways. <code>/chat/completions</code> reported
<code>prompt_tokens: 289</code>. <code>/anthropic/v1/messages</code> reported
<code>input_tokens: 33</code> with
<code>cache_read_input_tokens: 256</code>.</p>
<p><strong>33 + 256 = 289.</strong> On the Anthropic endpoint,
<code>input_tokens</code> <em>excludes</em> cache reads. On the OpenAI chat and
Responses endpoints, the input count <em>includes</em> them.</p>
</div>

<div class="tablewrap">
<table>
<thead><tr><th>Format</th><th>Full prompt is</th><th>Cached portion</th></tr></thead>
<tbody>
<tr><td>chat</td><td><code>prompt_tokens</code></td><td><code>prompt_cache_hit_tokens</code></td></tr>
<tr><td>anthropic</td><td><code>input_tokens</code> <strong>+</strong> <code>cache_read_input_tokens</code></td><td><code>cache_read_input_tokens</code></td></tr>
<tr><td>respond</td><td><code>input_tokens</code></td><td><code>input_tokens_details.cached_tokens</code></td></tr>
</tbody>
</table>
</div>

<p>Get it backwards and a heavily-cached Anthropic call looks 8&times; cheaper
than it was. <code>deepseek-cli</code> normalises all three into one shape
before pricing anything, and
<a href="{{repo}}/blob/main/internal/deepseek/usage_test.go">pins the
difference in tests</a> using those exact numbers.</p>

<h2 id="mapping">Claude model names</h2>
<p>The Anthropic endpoint accepts Claude model names and remaps them
server-side, which is what lets tools with hard-coded model lists work:</p>
<div class="tablewrap">
<table>
<thead><tr><th>You send</th><th>You get</th></tr></thead>
<tbody>
<tr><td><code>claude-opus-*</code></td><td><code>deepseek-v4-pro</code></td></tr>
<tr><td><code>claude-sonnet-*</code>, <code>claude-haiku-*</code></td><td><code>deepseek-v4-flash</code></td></tr>
<tr><td>anything unrecognised</td><td><code>deepseek-v4-flash</code></td></tr>
</tbody>
</table>
</div>
<p>Because billing follows the model that actually ran, the usage line prints
both names:</p>
<pre><code>$ ds anthropic "hello" --model claude-opus-4-1
&middot; claude-opus-4-1&rarr;pro &middot; 10 in &middot; 8 out &middot; ~$0.000011 &middot; 0.9s</code></pre>

<h2 id="reasoning">The reasoning round-trip</h2>
<p>In thinking mode the chain of thought comes back alongside the answer. What
you do with it on the next turn is not optional:</p>
<ul>
<li>If the request carried <code>tools</code>, every assistant message's
<code>reasoning_content</code> <strong>must</strong> be sent back on every
later request. Omit it and the API answers <code>400</code>.</li>
<li>If it did not, the field is ignored server-side &mdash; so replaying it
spends input tokens on text the model discards.</li>
</ul>
<p>Sessions in this CLI keep the reasoning stored either way, and decide per
request whether to put it on the wire.</p>

<h2 id="limits">Shared limits</h2>
<ul>
<li><strong>Text only.</strong> Image, document and search-result blocks are
rejected or replaced with placeholder text in every format.</li>
<li><strong>Thinking is on by default</strong> and costs a flat 79 extra input
tokens for its template &mdash; measured, and constant regardless of prompt
length &mdash; before a single reasoning token is generated.</li>
<li><strong>Slow starts are normal.</strong> The API holds the connection with
<code>: keep-alive</code> comments for up to ten minutes before inference
begins under load.</li>
<li><strong>One error envelope</strong> across all four:
<code>{"error":{"message","type","param","code"}}</code>.</li>
</ul>
""",
))

PAGES.append(dict(
    slug="cost/",
    crumb="cost",
    title="What DeepSeek actually costs — cache math and a local usage ledger",
    description="DeepSeek's context cache makes a cached input token 50x cheaper than an uncached one. How the deepseek CLI prices every call, what the local usage ledger records, and the caveats on every figure it prints.",
    keywords="deepseek pricing, deepseek api cost, deepseek context cache, deepseek cache hit tokens, deepseek token cost calculator, deepseek v4 flash price, deepseek usage tracking",
    jsonld=tech_article("What DeepSeek actually costs", "Pricing, context-cache savings, and the deepseek CLI usage ledger.", "cost/"),
    body="""
<h1>Cost</h1>
<p class="lede">DeepSeek's headline feature is a disk-backed context cache that
makes a repeated prompt prefix roughly fifty times cheaper. That saving is
invisible unless something is counting &mdash; so this counts.</p>

<h2 id="card">The rate card</h2>
<p>USD per 1M tokens, as published on 2026-08-02:</p>
<div class="tablewrap">
<table>
<thead><tr><th>Model</th><th class="num">Input (cached)</th><th class="num">Input (miss)</th><th class="num">Output</th></tr></thead>
<tbody>
<tr><td><code>deepseek-v4-flash</code></td><td class="num">$0.0028</td><td class="num">$0.14</td><td class="num">$0.28</td></tr>
<tr><td><code>deepseek-v4-pro</code></td><td class="num">$0.003625</td><td class="num">$0.435</td><td class="num">$0.87</td></tr>
</tbody>
</table>
</div>
<p><code>ds models</code> prints this next to the live model list, so the price
is on screen when you pick.</p>

<h2 id="cache">What the cache is worth</h2>
<p>On flash, a cache hit costs <strong>1/50th</strong> of a miss. The same
3,200-token prompt, sent twice:</p>
<div class="term">
<div class="term-bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span><span class="title">measured, not illustrative</span></div>
<pre><code><span class="p">$</span> ds chat <span class="w">"..."</span> <span class="k">--system</span> @prefix.txt
<span class="o">one</span>
<span class="c">&middot; flash &middot; 3.2k in &middot; 1 out &middot; ~$0.000450 &middot; 0.84s</span>

<span class="p">$</span> ds chat <span class="w">"..."</span> <span class="k">--system</span> @prefix.txt
<span class="o">two</span>
<span class="c">&middot; flash &middot; 3.2k in (100% cached) &middot; 1 out &middot; ~$0.000011 &middot; 1.04s</span></code></pre>
</div>
<p>A 40&times; drop, for changing nothing but sending the same prefix again.
The practical rule: <strong>put the stable part of a prompt first</strong>
&mdash; same system prompt, same files, in the same order &mdash; and let the
variable part come last.</p>

<h2 id="thinking">The thinking surcharge</h2>
<p>Thinking mode is on by default and costs a <strong>flat 79 extra input
tokens</strong> per request for its template, before generating a single
reasoning token. Measured across prompts of very different lengths:</p>
<div class="tablewrap">
<table>
<thead><tr><th>Prompt</th><th class="num">--think off</th><th class="num">--think on</th><th class="num">difference</th></tr></thead>
<tbody>
<tr><td><code>"hi"</code></td><td class="num">5</td><td class="num">84</td><td class="num">+79</td></tr>
<tr><td>9-token question</td><td class="num">11</td><td class="num">90</td><td class="num">+79</td></tr>
<tr><td>14-token question</td><td class="num">16</td><td class="num">95</td><td class="num">+79</td></tr>
</tbody>
</table>
</div>
<p>On a short factual lookup that surcharge is most of the bill, and the
reasoning tokens it then generates are the rest. <code>--think off</code>
removes both.</p>

<h2 id="ledger">The ledger</h2>
<p>Every call prints one line to stderr and appends one row to
<code>~/.local/state/deepseek/usage.jsonl</code>:</p>
<pre><code>{"ts":"2026-08-05T05:18:12Z","api":"chat","model":"deepseek-v4-flash",
 "in":3242,"cache_hit":3200,"cache_miss":42,"out":1,
 "cost_usd":0.0000109,"saved_usd":0.000439,"ms":1041}</code></pre>
<p>Token counts are exact and are what gets stored; the cost field is a
convenience. That is deliberate &mdash; when DeepSeek changes the rate card,
every historical row can be repriced.</p>

<pre><code>ds usage                  # today
ds usage --since 7d
ds usage --since all --json
ds usage --entries        # individual calls</code></pre>

<div class="term">
<div class="term-bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span><span class="title">ds usage --since 7d</span></div>
<pre><code>                   CALLS  IN     CACHED  OUT    COST
deepseek-v4-flash  184    2.1M   78%     94k    $0.19
deepseek-v4-pro    12     88k    41%     11k    $0.03
total              196    2.2M   77%     105k   $0.22

<span class="c">by format: chat 170, anthropic 14, responses 8, fim 4</span>
<span class="c">context cache saved ~$0.23 (1.7M of 2.2M prompt tokens replayed)</span>
<span class="c">costs are estimates from the published USD rate card, not billed amounts</span></code></pre>
</div>

<p>The savings line is the one worth watching. It is what the cached tokens
<em>would</em> have cost at the miss rate, minus what they did cost &mdash;
which is the number that tells you whether prompt structuring is paying off.</p>

<h2 id="caveats">What these numbers are not</h2>
<div class="note warn">
<span class="tag">read this before quoting a figure</span>
<ul>
<li><strong>Estimates, not invoices.</strong> Computed from the published USD
rate card. Your account may bill in another currency &mdash;
<code>ds balance</code> shows which.</li>
<li><strong>Peak pricing is not applied.</strong> DeepSeek has announced a 2&times;
multiplier for 09:00&ndash;12:00 and 14:00&ndash;18:00 Beijing time, with no
effective date. Applying it now would double every estimate on a guess, so it
is deliberately left out until the date is announced.</li>
<li><strong>Local only.</strong> The ledger records calls made by this CLI on
this machine. It knows nothing about your other clients.</li>
</ul>
</div>
<p><code>--no-ledger</code> skips the write, <code>--no-stats</code> hides the
line, and neither ever fails the command that produced it &mdash; you asked for
a completion, not for bookkeeping.</p>
""",
))

PAGES.append(dict(
    slug="agents/",
    crumb="agents",
    title="deepseek-cli for agents and scripts — the output contract",
    description="How to drive the deepseek CLI from code: stdout/stderr split, unwrapped --json responses, per-format JSON shapes, meaningful exit codes, retry semantics, and a drop-in agent skill.",
    keywords="deepseek cli scripting, deepseek cli json output, deepseek agent skill, deepseek cli exit codes, llm cli automation, deepseek jq",
    jsonld=tech_article("deepseek-cli for agents and scripts", "The scripting contract for the deepseek CLI.", "agents/"),
    body="""
<h1>For agents</h1>
<p class="lede">Built to be driven by other programs. The contract below is
stable and tested; the same text ships in the repo as
<a href="{{repo}}/blob/main/AGENTS.md">AGENTS.md</a> and as a drop-in
<a href="{{repo}}/blob/main/skill/SKILL.md">agent skill</a>.</p>

<h2 id="contract">The output contract</h2>
<ul>
<li><strong>stdout is data.</strong> The answer text, or with
<code>--json</code> the API's own response body.</li>
<li><strong>stderr is status.</strong> Chain of thought, the usage line,
truncation warnings, verbose HTTP, errors.</li>
<li><code>ds chat "..." &gt; answer.txt</code> writes the answer and nothing
else.</li>
</ul>

<h2 id="json">--json is not wrapped</h2>
<p><code>--json</code> prints the API's response byte-for-byte: no envelope, no
injected fields. jq recipes written against the OpenAI or Anthropic APIs keep
working unchanged.</p>
<pre><code>ds chat "..." --json | jq -r '.choices[0].message.content'
ds chat "..." --jq '.usage'
ds models --json | jq -r '.data[].id'</code></pre>
<p>Cost deliberately does not appear there &mdash; it would have meant wrapping
the response. It goes to stderr and to the
<a href="{{root}}cost/">ledger</a> instead.</p>

<h2 id="shapes">Response shapes</h2>
<p>From the API, so they are the documented DeepSeek shapes:</p>
<pre><code>chat       {"id","object","created","model",
            "choices":[{"message":{"content","reasoning_content","tool_calls"},
                        "finish_reason"}],"usage":{...}}
anthropic  {"id","type","role","model","content":[{"type","text"}],
            "stop_reason","usage":{...}}
respond    {"id","object","status","model",
            "output":[{"type","content":[{"type","text"}]}],"usage":{...}}
fim        {"id","object","model","choices":[{"text","finish_reason"}],"usage":{...}}
models     {"object":"list","data":[{"id","object","owned_by"}]}
balance    {"is_available","balance_infos":[{"currency","total_balance",...}]}</code></pre>
<p>Computed locally, not from the API:</p>
<pre><code>usage      {"since","total":{...},"by_model":{...},"by_api":{...}}
check      {"base_url","key_set","ok",
            "probes":[{"name","path","ok","detail","error","ms"}]}
session ls [{"name","model","turns","updated","bytes"}]</code></pre>
<p>Note that usage field names differ per format, exactly as the API sends
them. See <a href="{{root}}formats/#tokens">the token-accounting trap</a>.</p>

<h2 id="exit">Exit codes carry meaning</h2>
<pre><code>0   success
1   error            read stderr
2   auth             key missing or rejected  &rarr; do not retry
3   no balance       top up                   &rarr; do not retry
4   rate limited     back off, then retry
130 interrupted</code></pre>
<p>Transport failures and 429/5xx are already retried internally with
exponential backoff, honouring <code>Retry-After</code>. A non-zero exit means
your own retry loop probably will not help either. Requests that reached the
model are never retried &mdash; a second call would be billed twice.</p>

<h2 id="preflight">Preflight</h2>
<pre><code>ds check --json
# {"base_url":"...","key_set":true,"probes":[...],"ok":true}</code></pre>
<p>One command, six endpoints, a fraction of a cent. If <code>ok</code> is
false, read <code>probes[].error</code>.</p>

<h2 id="errors">Errors say what to do</h2>
<pre><code>$ ds chat hi
Error: Insufficient Balance (HTTP 402)
  out of balance: deepseek balance &mdash; top up at https://platform.deepseek.com/top_up</code></pre>

<h2 id="quiet">Quiet mode</h2>
<pre><code>ds chat "..." --no-stats --no-ledger --json</code></pre>
<p>Use both when the usage line would pollute captured output, or when a batch
run should not land in the ledger.</p>

<h2 id="skill">As an agent skill</h2>
<p><a href="{{repo}}/blob/main/skill/SKILL.md">skill/SKILL.md</a> ships in every
release archive. It carries the same contract as an operational procedure, with
the rules that matter for autonomous use:</p>
<ul>
<li>Report costs as estimates, never as billed amounts.</li>
<li>Do not retry on exit 2 or 3 &mdash; bring them to the human.</li>
<li>The API is text-only: no images, no documents.</li>
<li>Slow starts of up to ten minutes are normal under load, not a failure.</li>
</ul>
""",
))


def build(check_only=False):
    written, stale = [], []
    for page in PAGES:
        out = ROOT / page["slug"] / "index.html" if page["slug"] else ROOT / "index.html"
        html_text = render(page)
        if check_only:
            if not out.exists() or out.read_text() != html_text:
                stale.append(str(out.relative_to(ROOT)))
            continue
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(html_text)
        written.append(str(out.relative_to(ROOT)))

    # sitemap: every page, in reading order.
    urls = "\n".join(
        f"  <url><loc>{SITE}/{p['slug']}</loc>"
        f"<changefreq>weekly</changefreq>"
        f"<priority>{'1.0' if not p['slug'] else '0.8'}</priority></url>"
        for p in PAGES
    )
    sitemap = (
        '<?xml version="1.0" encoding="UTF-8"?>\n'
        '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n'
        f"{urls}\n</urlset>\n"
    )
    robots = (
        "User-agent: *\n"
        "Allow: /\n\n"
        f"Sitemap: {SITE}/sitemap.xml\n"
    )

    for name, text in (("sitemap.xml", sitemap), ("robots.txt", robots)):
        out = ROOT / name
        if check_only:
            if not out.exists() or out.read_text() != text:
                stale.append(name)
        else:
            out.write_text(text)
            written.append(name)

    # 404: served from any depth, so every path in it is absolute.
    notfound = render(dict(
        slug="", crumb="",
        title="Not found — deepseek-cli",
        description="That page does not exist. The deepseek-cli documentation index.",
        keywords="deepseek cli",
        jsonld=SOFTWARE_JSONLD,
        body="""
<h1>404</h1>
<p class="lede">No such page. The whole site is six of them:</p>
<ul>
<li><a href="BASE/">overview</a> &mdash; what it is and why</li>
<li><a href="BASE/install/">install</a> &mdash; binaries, Go, the <code>ds</code> alias, configuration</li>
<li><a href="BASE/commands/">commands</a> &mdash; every command and flag</li>
<li><a href="BASE/formats/">formats</a> &mdash; DeepSeek's four wire formats compared</li>
<li><a href="BASE/cost/">cost</a> &mdash; pricing, the context cache, the usage ledger</li>
<li><a href="BASE/agents/">agents</a> &mdash; the scripting contract</li>
</ul>
<p><a href="BASE/">&larr; back to the overview</a></p>
""",
    ))
    # Rewrite the relative links a depth-0 page would emit into absolute
    # ones, since a 404 can be served from any URL.
    notfound = notfound.replace('href="./', f'href="{SITE}/').replace('href="BASE/', f'href="{SITE}/')
    # A 404 is not a page: drop the canonical it inherited from the
    # homepage template, and keep it out of the index.
    notfound = notfound.replace(
        f'<link rel="canonical" href="{SITE}/">',
        '<meta name="robots" content="noindex, follow">')
    notfound = notfound.replace('href="style.css"', f'href="{SITE}/style.css"')
    notfound = notfound.replace('href="favicon.svg"', f'href="{SITE}/favicon.svg"')
    out404 = ROOT / "404.html"
    if check_only:
        if not out404.exists() or out404.read_text() != notfound:
            stale.append("404.html")
    else:
        out404.write_text(notfound)
        written.append("404.html")

    if check_only:
        if stale:
            print("stale (run `python3 site/build.py`):", ", ".join(stale), file=sys.stderr)
            return 1
        print(f"site up to date ({len(PAGES)} pages)")
        return 0

    print(f"wrote {len(written)} files: {', '.join(written)}")
    return 0


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--check", action="store_true", help="fail if the committed HTML is stale")
    sys.exit(build(check_only=ap.parse_args().check))
