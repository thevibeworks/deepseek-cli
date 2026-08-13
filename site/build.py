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
import subprocess
import sys

SITE = "https://thevibeworks.github.io/deepseek-cli"
REPO = "https://github.com/thevibeworks/deepseek-cli"
DOCS = "https://api-docs.deepseek.com"
OG_IMAGE = f"{SITE}/og.png"
ROOT = pathlib.Path(__file__).resolve().parent


def _release() -> str:
    """The release the masthead shows, read from git at build time.

    Asking git rather than hardcoding it means the figure cannot rot
    silently: the moment a new tag lands, `--check` fails until the HTML
    is rebuilt. The constant below is only for building outside a git
    checkout, where there is no tag to ask for.
    """
    try:
        out = subprocess.run(
            ["git", "describe", "--tags", "--abbrev=0"],
            capture_output=True, text=True, cwd=ROOT, timeout=10,
        )
        tag = out.stdout.strip()
        if out.returncode == 0 and tag:
            return tag
    except OSError:
        pass
    return "v0.5.0"


RELEASE = _release()

# Cloudflare Turnstile sitekey for the playground's enrol step. Empty
# means the widget does not exist: no third-party script, no container,
# and playground.js takes its no-op path. Set it (and give the gateway
# the matching DSGATE_TURNSTILE_SECRET) to require the browser check on
# top of the proof-of-work for browser enrolments. The sitekey is
# public by design; the secret never appears in this repository.
TURNSTILE_SITEKEY = ""

# Every page, in reading order. The order drives the nav, the pager links
# and the sitemap, so there is exactly one list to keep correct.
NAV = [
    ("", "overview"),
    ("install/", "install"),
    ("commands/", "commands"),
    ("formats/", "formats"),
    ("cost/", "cost"),
    ("bench/", "bench"),
    ("news/", "news"),
    ("agents/", "agents"),
    ("playground/", "playground"),
]

# Theme. The default is whatever the OS says; the toggle overrides it and
# persists the override. Two scripts, and the split matters:
#
# THEME_INIT runs in <head>, before the first paint, so a reader who chose
# light does not get a black flash on every navigation. It must therefore
# be inline and tiny – an external file would be a blocking round trip.
# It touches only documentElement, which exists by then.
#
# THEME_TOGGLE runs at the end of <body>, where the button exists. It also
# unhides the button: a control that does nothing without JS should not
# take up space when there is none.
#
# localStorage throws in Safari's private mode rather than returning null,
# so both are wrapped. A thrown init script would leave the page unstyled.
THEME_INIT = (
    "try{var t=localStorage.getItem('theme');"
    "if(t==='dark'||t==='light')document.documentElement.dataset.theme=t}catch(e){}"
)

THEME_TOGGLE = """
(function () {
  var b = document.getElementById('theme-toggle');
  if (!b) return;
  var order = ['auto', 'light', 'dark'];
  var read = function () {
    try { return localStorage.getItem('theme') || 'auto'; } catch (e) { return 'auto'; }
  };
  var paint = function (v) {
    if (v === 'auto') delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = v;
    b.querySelector('.val').textContent = v;
    // The button reads "theme: dark" to a screen reader, and announcing
    // the change is the whole feedback – the visual change is silent.
    b.setAttribute('aria-label', 'Theme: ' + v + '. Click to change.');
  };
  b.hidden = false;
  paint(read());
  b.addEventListener('click', function () {
    var next = order[(order.indexOf(read()) + 1) % order.length];
    try { localStorage.setItem('theme', next); } catch (e) {}
    paint(next);
  });
})();
""".strip()


def head(*, slug, title, description, keywords, jsonld, crumb_title,
         band_sea=False):
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

    # Every page gets the fixed full-viewport sea behind it, except a page
    # that asks for the band: the 404 carries its sea as a strip under the
    # masthead, so the whale rides the header rule like a horizon and the
    # wayfinding links below it keep their contrast.
    # The depth gauge rides with the fixed sea and only with it. It reads
    # how far down the water column you have scrolled, and a band page has
    # no column to descend -- a gauge there would be an instrument wired to
    # nothing. waves.js treats it as optional for the same reason.
    sea_fixed = (
        ""
        if band_sea
        else '<div class="sea" data-ocean></div>\n'
        '<div class="gauge" data-depth-gauge aria-hidden="true">'
        '<span class="gauge-rail"><span class="gauge-fill"></span></span>'
        '<span class="gauge-read">0 m</span></div>\n'
    )
    sea_band = '<div class="sea sea-band" data-ocean="band"></div>' if band_sea else ""

    return f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(title)}</title>
<meta name="description" content="{html.escape(description)}">
<meta name="keywords" content="{html.escape(keywords)}">
<meta name="author" content="thevibeworks">
<meta name="theme-color" content="#fbfaf7" media="(prefers-color-scheme: light)">
<meta name="theme-color" content="#000000" media="(prefers-color-scheme: dark)">
<link rel="canonical" href="{url}">
<meta property="og:type" content="website">
<meta property="og:site_name" content="deepseek-cli">
<meta property="og:title" content="{html.escape(title)}">
<meta property="og:description" content="{html.escape(description)}">
<meta property="og:url" content="{url}">
<meta property="og:image" content="{OG_IMAGE}">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta property="og:image:alt" content="deepseek-cli: the whole DeepSeek API from the terminal">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="{html.escape(title)}">
<meta name="twitter:description" content="{html.escape(description)}">
<meta name="twitter:image" content="{OG_IMAGE}">
<link rel="icon" href="{root}favicon.svg" type="image/svg+xml">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="{root}style.css">
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@300;400;700&display=swap" rel="stylesheet">
<script>{THEME_INIT}</script>
<script type="application/ld+json">
{jsonld}
</script>
<script type="application/ld+json">
{{"@context":"https://schema.org","@type":"BreadcrumbList","itemListElement":[{",".join(crumbs)}]}}
</script>
</head>
<body>
{sea_fixed}<a class="skip" href="#main">skip to content</a>
<header class="masthead">
  <div class="wrap">
    <a class="brand" href="{root or './'}">
      <span class="caret">&gt;</span><svg class="mark" viewBox="0 0 63.1196 46.4033" width="23" height="17" aria-hidden="true"><path d="M62.4575 3.89441C61.7888 3.56726 61.501 4.1908 61.1101 4.50769C60.9763 4.60999 60.863 4.7428 60.75 4.86548C59.7727 5.9082 58.6311 6.59302 57.1394 6.51123C54.9587 6.38855 53.0969 7.07349 51.4512 8.73975C51.1013 6.68506 49.939 5.45837 48.1699 4.67126C47.2441 4.26233 46.3081 3.85352 45.6599 2.96411C45.2073 2.33032 45.084 1.625 44.8577 0.929932C44.7136 0.510864 44.5696 0.081543 44.0862 0.0098877C43.5615 -0.0718994 43.3557 0.367676 43.1501 0.735718C42.3271 2.2384 42.0083 3.89441 42.0391 5.5708C42.1111 9.34277 43.7056 12.3481 46.8738 14.4846C47.2336 14.73 47.3264 14.9753 47.2131 15.333C46.9971 16.0691 46.74 16.7847 46.5137 17.5206C46.3696 17.9908 46.1538 18.093 45.6497 17.8887C43.9114 17.1628 42.4094 16.0895 41.0825 14.7913C38.8298 12.6139 36.7932 10.2117 34.2524 8.33081C33.6558 7.89124 33.0593 7.48242 32.4421 7.09399C29.8499 4.57922 32.7815 2.5144 33.4604 2.26904C34.1702 2.01343 33.7073 1.1344 31.4133 1.14465C29.1196 1.15479 27.0212 1.92151 24.3467 2.94373C23.9558 3.09705 23.5444 3.20947 23.1226 3.30151C20.6951 2.84143 18.1748 2.73926 15.5415 3.03577C10.5835 3.58777 6.62329 5.92859 3.7124 9.92554C0.215088 14.73 -0.60791 20.1886 0.400146 25.8824C1.45972 31.8828 4.5249 36.8508 9.23608 40.7354C14.1221 44.7629 19.7488 46.7357 26.1675 46.3575C30.0659 46.1327 34.4067 45.6113 39.303 41.4713C40.5374 42.0847 41.8335 42.33 43.9834 42.514C45.6394 42.6674 47.2336 42.4323 48.468 42.1766C50.4019 41.7678 50.2683 39.9789 49.5688 39.6517C43.9009 37.0144 45.1455 38.0878 44.0142 37.2189C46.8943 33.8148 51.2351 30.278 52.9324 18.8188C53.0662 17.9091 52.9529 17.3367 52.9324 16.6006C52.9221 16.1509 53.0249 15.9771 53.5393 15.9259C54.9587 15.7625 56.3372 15.3739 57.6023 14.6788C61.2747 12.6753 62.7559 9.38367 63.1055 5.43799C63.157 4.83484 63.0952 4.2113 62.4575 3.89441ZM30.4568 39.4065C24.9639 35.0927 22.2998 33.6718 21.199 33.7332C20.1704 33.7944 20.3557 34.97 20.5818 35.7367C20.8186 36.493 21.1272 37.0144 21.5591 37.6788C21.8574 38.1184 22.0632 38.7727 21.2607 39.2633C19.4915 40.3571 16.416 38.8953 16.272 38.8237C12.6924 36.718 9.69897 33.9375 7.59033 30.1349C5.55347 26.4753 4.37061 22.5499 4.17529 18.3589C4.12378 17.3468 4.42212 16.989 5.43018 16.8051C6.75708 16.5597 8.12524 16.5087 9.45215 16.7029C15.0581 17.5206 19.8311 20.025 23.8323 23.9913C26.116 26.2504 27.844 28.9491 29.6235 31.5864C31.5164 34.3873 33.553 37.0553 36.145 39.2429C37.0605 40.0095 37.791 40.5922 38.4905 41.0215C36.3816 41.2567 32.8638 41.3077 30.4568 39.4065ZM33.0901 22.4886C33.0901 22.0388 33.4502 21.681 33.9026 21.681C34.0056 21.681 34.0981 21.7015 34.1804 21.7322C34.2935 21.7731 34.3965 21.8344 34.4788 21.9264C34.6228 22.0695 34.7051 22.2739 34.7051 22.4886C34.7051 22.9384 34.345 23.2961 33.8923 23.2961C33.4397 23.2961 33.0901 22.9384 33.0901 22.4886ZM41.2676 26.6798C40.7432 26.8944 40.2185 27.0784 39.7144 27.0989C38.9326 27.1398 38.0789 26.8229 37.616 26.4344C36.896 25.8313 36.3816 25.494 36.1658 24.441C36.073 23.9913 36.1245 23.2961 36.2068 22.8975C36.3921 22.0388 36.1863 21.4868 35.5793 20.986C35.0857 20.577 34.4583 20.4646 33.769 20.4646C33.5117 20.4646 33.2751 20.3522 33.1003 20.2601C32.8123 20.1171 32.5757 19.7593 32.802 19.3197C32.874 19.1766 33.2239 18.8291 33.3062 18.7677C34.2422 18.2362 35.3223 18.4099 36.3201 18.8086C37.2458 19.1869 37.9453 19.882 38.9534 20.8633C39.9819 22.0491 40.167 22.3762 40.7534 23.2655C41.2163 23.9607 41.6379 24.6761 41.926 25.494C42.1008 26.0051 41.8745 26.4242 41.2676 26.6798Z"/></svg><span class="org">thevibeworks/</span><span class="name">deepseek-cli</span>
    </a>
    <!-- Nav and toggle travel together – they are the controls end of the
         masthead – but the toggle is beside the nav rather than inside it.
         Two reasons, and the second is the load-bearing one: a theme
         preference is not a section of the site, and on a phone the nav
         becomes a scrolling strip with a mask on it, which would take the
         toggle with it. A control nobody can see is worse than a control
         in the wrong list. -->
    <div class="bar-end">
      <nav class="sitenav" aria-label="Sections">
{nav_links(slug, root)}
        <a href="{REPO}">github&nbsp;&#8599;</a>
        <a href="{REPO}/releases">{RELEASE}</a>
      </nav>
      <button class="themetoggle" id="theme-toggle" type="button" hidden
              aria-label="Theme: auto. Click to change.">theme:&nbsp;<span class="val">auto</span></button>
    </div>
  </div>
</header>
{sea_band}<main id="main">
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
    <span>unofficial &ndash; not affiliated with DeepSeek</span>
  </div>
</footer>
<script>
{toggle}
</script>
<script src="{root}waves.js" defer></script>
{extra}</body>
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
        band_sea=page.get("band_sea", False),
    )
    body += crumb(page.get("crumb", ""), root)
    body += page["body"].replace("{{root}}", root or "./").replace("{{repo}}", REPO).replace("{{docs}}", DOCS)
    body += pager(slug, root)
    body += FOOT.format(
        repo=REPO, site=SITE, toggle=THEME_TOGGLE, root=root or "./",
        # Pages that ship an app rather than prose load it here, after the
        # theme script, so the markup it wires up already exists.
        extra=page.get("scripts", "").replace("{{root}}", root or "./"),
    )
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
    title="deepseek-cli: the whole DeepSeek API from the terminal",
    description="A single Go binary for every DeepSeek API: chat completions in OpenAI, Anthropic and Responses formats, FIM, models and balance, with multi-turn that survives the reasoning round-trip and per-call cost accounting.",
    keywords="deepseek cli, deepseek api, deepseek command line, deepseek-v4-flash, deepseek-v4-pro, deepseek anthropic api, deepseek responses api, deepseek context cache, deepseek pricing, llm cli",
    jsonld=SOFTWARE_JSONLD,
    body="""
<section class="hero">
<h1><span class="caret">&gt;</span> deepseek-cli</h1>
<p class="lede">DeepSeek serves the same two models through four different wire
formats. Every other client picks one. This speaks all four &ndash; from one
binary, with the multi-turn bookkeeping kept straight and a running tally of
what each call cost.</p>

<p class="badges">
<img src="https://img.shields.io/github/v/release/thevibeworks/deepseek-cli?color=00c2e9&labelColor=0d0d0d&label=release" alt="Latest release" width="104" height="20" loading="lazy">
<img src="https://img.shields.io/badge/API%20coverage-6%2F6%20endpoints-00ff41?labelColor=0d0d0d" alt="API coverage: 6 of 6 endpoints" width="188" height="20" loading="lazy">
<img src="https://img.shields.io/badge/tests-302%20%C2%B7%2070%25%20covered-00ff41?labelColor=0d0d0d" alt="302 tests, 70 percent covered" width="166" height="20" loading="lazy">
<img src="https://img.shields.io/badge/DeepSeek%20API%20docs-2026--08--05-bf00ff?labelColor=0d0d0d" alt="Built against the DeepSeek API docs of 2026-08-05" width="196" height="20" loading="lazy">
<img src="https://img.shields.io/badge/models-v4--flash%20%7C%20v4--pro-00c2e9?labelColor=0d0d0d" alt="Models: deepseek-v4-flash and deepseek-v4-pro" width="164" height="20" loading="lazy">
</p>

<div class="cta">
<a class="btn" href="{{root}}install/">install</a>
<a class="btn alt" href="{{root}}playground/">try it, no key</a>
<a class="btn alt" href="{{root}}commands/">commands</a>
<a class="btn alt" href="{{repo}}">source</a>
</div>
</section>

<div class="term">
<div class="term-bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span><span class="title">~/work</span></div>
<pre><code><span class="p">$</span> ds chat <span class="w">"explain this diff"</span> <span class="k">--file</span> changes.patch
<span class="o">The patch swaps the retry loop for exponential backoff, and stops
retrying 4xx responses &ndash; those would fail identically on a second try.</span>
<span class="c">&middot; flash &middot; 3.2k in (87% cached) &middot; 412 out (180 think) &middot; ~$0.000178 &middot; 2.1s</span></code></pre>
</div>

<h2 id="no-key">You do not need an API key to start</h2>
<p class="lede">Most tools open with "get an API key". This one opens with an
answer.</p>

<div class="term">
<div class="term-bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span><span class="title">~/work</span></div>
<pre><code><span class="p">$</span> deepseek free
<span class="o">The free tier relays your prompts to DeepSeek through a gateway run by
this project. No account, no API key.</span>

<span class="c">  gateway   https://freeseek.1lm.io
  model     deepseek-v4-flash
  per day   30 requests &middot; 60k input &middot; 20k output tokens
  privacy   prompts are relayed, not stored; only token counts are recorded</span>

<span class="o">Minting an anonymous token (20 bits of proof-of-work)&hellip;</span>
<span class="c">  solved 20 bits in 0.4s (1.0M hashes)</span>

<span class="o">Enrolled.</span>

<span class="p">$</span> deepseek chat <span class="w">"why is the sky blue"</span>
<span class="o">Sunlight is scattered by the atmosphere, and shorter wavelengths scatter
far more &ndash; Rayleigh scattering.</span>
<span class="c">&middot; flash &middot; 93 in &middot; 46 out (7 think) &middot; ~$0.000026 &middot; 1.56s</span></code></pre>
</div>

<p>About a second of CPU stands in for the signup &ndash; a proof-of-work
puzzle, which is the whole enrolment. No email, no card, no dashboard. There is
a <a href="{{root}}playground/">browser playground</a> on the same free tier
that shows you the equivalent command for whatever you set up in it.</p>

<p>A real API key always takes precedence, so this is a fallback for not having
one rather than a way around having one. The gateway is
<a href="{{repo}}/tree/main/gateway">in the repository</a> and meant to be
self-hostable; its <a href="{{repo}}/blob/main/gateway/DESIGN.md">design
notes</a> include the part most services leave out &ndash; why per-user quota
is <em>not</em> what keeps it solvent, and what is.</p>

<h2 id="why">Why this exists</h2>
<p>Wrapping six HTTP endpoints is not interesting on its own. Two things are,
and they are the reason this is not a shell function around <code>curl</code>.</p>

<ul class="grid">
<li class="card">
<h3>Multi-turn that does not 400</h3>
<p>With tools in play, DeepSeek rejects any request that fails to replay every
assistant <code>reasoning_content</code>. Without tools it ignores the same
field &ndash; so sending it just burns input tokens. <strong>Sessions get both
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
<h3>It carries the manual</h3>
<p>Every page of DeepSeek's API docs lives <em>inside</em> the binary, plus
the FAQ that is otherwise locked in a JavaScript bundle.
<code>ds docs ask</code> answers from them and
<strong>cites the page</strong> &ndash; so the answer is checkable against a
URL, not whatever a model remembers about an API that changes monthly.</p>
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

<h2 id="selfhost">The API, explaining itself</h2>
<p>The tool that talks to an API should be able to answer questions about
it. This one carries DeepSeek's own documentation &ndash; 67 pages,
about 85KB compressed &ndash; and asks DeepSeek to answer from it.</p>

<div class="term">
<div class="term-bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span><span class="title">ds docs ask</span></div>
<pre><code><span class="p">$</span> ds docs ask <span class="w">"when must I send reasoning_content back?"</span>
<span class="o">Send reasoning_content back only when the model performed a tool call
during that turn. In that case it must be passed back in all subsequent
turns, or the API returns a 400 error (guides/thinking_mode).</span>
<span class="c">answered from guides/thinking_mode, api/create-chat-completion &middot; docs built in, fetched today</span>
<span class="c">&middot; flash &middot; 5.3k in (39% cached) &middot; 116 out &middot; ~$0.000778 &middot; 2.2s</span></code></pre>
</div>

<p>Pages are selected locally and sent whole, with an instruction to answer
only from them. The same pages lead every request, so the second question
about an area <strong>hits the context cache</strong> &ndash; which is the
cost feature on this site demonstrating itself. Search, read and the
change log cost nothing and need no network:</p>

<pre><code>ds docs search "context cache"
ds docs show guides/kv_cache
ds docs changelog              # what DeepSeek shipped, newest first
ds docs sync                   # refresh the snapshot</code></pre>

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
<tr><td><code>fim</code></td><td><code>POST /beta/completions</code></td><td>Fill in the middle &ndash; the shape editors use for inline completion.</td></tr>
<tr><td><code>models</code></td><td><code>GET /models</code></td><td>Available models, joined with the published rate card.</td></tr>
<tr><td><code>balance</code></td><td><code>GET /user/balance</code></td><td>What is left, per currency.</td></tr>
<tr><td><code>tokens</code></td><td><code>POST /beta/completions</code></td><td>Exact token counts, from the model's own tokenizer.</td></tr>
<tr><td><code>docs</code></td><td><em>local</em></td><td>DeepSeek's own documentation, in the binary. Search, read, ask.</td></tr>
<tr><td><code>usage</code></td><td><em>local</em></td><td>What this CLI has spent, from its own ledger.</td></tr>
<tr><td><code>session</code></td><td><em>local</em></td><td>The conversations <code>chat --continue</code> replays.</td></tr>
<tr><td><code>status</code></td><td><code>GET /models</code>, <code>/user/balance</code></td><td>Is it up, for this key, from here. Costs nothing.</td></tr>
<tr><td><code>check</code></td><td><em>all six</em></td><td>Preflight.</td></tr>
<tr><td><code>raw</code></td><td><em>anything</em></td><td>Escape hatch &ndash; any path, with auth and retries.</td></tr>
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
search-result content blocks in every format &ndash; that is the API, not a
gap here.</li>
</ul>
""",
))

PAGES.append(dict(
    slug="install/",
    crumb="install",
    title="Install deepseek-cli: binaries, Go, and the ds alias",
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
would rather read it first &ndash; and you should, for anything piped to a
shell &ndash; it is <a href="{{repo}}/blob/main/install.sh">forty lines</a>.</p>

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
<tr><td><code>DEEPSEEK_API_KEY</code></td><td>API key</td><td>&ndash;</td></tr>
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
<p>Calls all six endpoints once with a one-token cap &ndash; the whole check
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
    title="deepseek-cli command reference: chat, anthropic, respond, fim, usage",
    description="Full reference for every deepseek CLI command and flag: chat completions, Anthropic Messages, OpenAI Responses, FIM, models, balance, usage ledger, sessions, check and raw, plus exit codes.",
    keywords="deepseek cli commands, deepseek chat completions cli, deepseek fim completion, deepseek cli flags, deepseek cli reference, deepseek json output, deepseek cli exit codes",
    jsonld=tech_article("deepseek-cli command reference", "Every command and flag in the deepseek CLI.", "commands/"),
    body="""
<h1>Commands</h1>
<p class="lede">Thirteen commands: seven wrap an endpoint, three read
local state, two are health checks, one is an escape hatch.
<code>--help</code> on any of them carries the same detail.</p>

<h2 id="chat">chat</h2>
<p><code>POST /chat/completions</code> &ndash; the default door, and the only
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

<h3 id="interactive">Interactive</h3>
<p><code>-i</code> keeps the conversation open and prompts for the next
turn, instead of retyping <code>--continue</code>:</p>
<div class="term">
<div class="term-bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span><span class="title">ds chat -i</span></div>
<pre><code><span class="p">$</span> ds chat <span class="k">-i</span> <span class="w">"walk me through this codebase"</span> <span class="k">--file</span> main.go
<span class="o">The entry point wires three things together...</span>
<span class="c">&middot; flash &middot; 2.1k in &middot; 180 out &middot; ~$0.000302 &middot; 1.8s</span>
<span class="p">&rsaquo;</span> now what would you change first
<span class="o">The retry loop, because...</span>
<span class="p">&rsaquo;</span> <span class="k">/model pro</span>
<span class="c">model deepseek-v4-pro</span>
<span class="p">&rsaquo;</span> <span class="c">^D</span>
<span class="c">bye &ndash; 4 messages saved as "last"; resume with: deepseek chat -c</span></code></pre>
</div>
<p>It is the same session machinery, so nothing is lost on exit &ndash;
<code>ds chat -c</code> resumes it and <code>ds session show last</code>
reads it. <code>/help</code> lists the slash commands: <code>/model</code>,
<code>/think</code>, <code>/effort</code>, <code>/system</code>,
<code>/file</code>, <code>/tokens</code>, <code>/docs</code>,
<code>/new</code>, <code>/save</code>.</p>
<p><code>^C</code> during an answer abandons that answer and keeps the
conversation; <code>^D</code> leaves. It needs a terminal and refuses to
combine with <code>--json</code> &ndash; for scripted multi-turn use
<code>--session</code>, which is what it is built on.</p>

<h3 id="tools">Tools</h3>
<pre><code>ds chat "weather in Hangzhou?" --tool @weather.json
# tool_call call_00_hUj... get_weather({"city": "Hangzhou"})</code></pre>
<p>One tool file works against every format &ndash; both the OpenAI
<code>parameters</code> and the Anthropic <code>input_schema</code> spellings
are accepted. The calls are printed, never executed.</p>

<h2 id="anthropic">anthropic</h2>
<p><code>POST /anthropic/v1/messages</code> &ndash; the format Claude Code, the
Anthropic SDKs and the Claude desktop app speak.</p>
<pre><code>ds anthropic "hello"
ds anthropic "hello" --model claude-opus-4-1 --json</code></pre>
<p>Claude model names are accepted and remapped server-side. The usage line
shows both names so the cost stays traceable to the model that actually ran.
See <a href="{{root}}formats/">formats</a> for the mapping.</p>

<h2 id="respond">respond</h2>
<p><code>POST /responses</code> &ndash; the format Codex speaks. Two things
live only here: JSON Schema structured output, and a <code>web_search</code>
tool DeepSeek runs server-side.</p>
<pre><code>ds respond "what shipped in Go 1.26" --web-search
ds respond "Berlin" -s "Return city and country." --schema @city.json</code></pre>
<p><code>--web-search</code> is the whole setup: the search runs on
DeepSeek's side, there is nothing to execute locally, and the searches the
model makes are reported on stderr as they happen. It is the one tool in the
whole API that this CLI can &ldquo;run&rdquo; for you, because DeepSeek runs
it. The API ignores the OpenAI knobs (<code>search_context_size</code>,
<code>user_location</code>), and in multi-turn use the server restores
search results replayed from earlier turns by itself.</p>
<p>Both models, since V4-Pro's official release &ndash; it was flash-only
before 2026-08-12.</p>

<h2 id="fim">fim</h2>
<p><code>POST /beta/completions</code> &ndash; give it a prefix and an optional
suffix; it writes the middle.</p>
<pre><code>ds fim "def add(a, b):" --suffix "    return result"
ds fim --prefix @head.go --suffix @tail.go --max-tokens 200</code></pre>
<p>Beta, with two hard limits: output caps at 4K tokens, and it never thinks.</p>

<h2 id="tokens">tokens</h2>
<p>Exact token counts, from the tokenizer that will bill you.</p>
<pre><code>ds tokens "why is the sky blue"
ds tokens --file main.go --file main_test.go
git diff | ds tokens
ds tokens --offline --file huge.log     # free local estimate</code></pre>
<p>DeepSeek ships no count-tokens endpoint and no Go tokenizer &ndash; only a
Python demo and two rules of thumb. But the FIM endpoint takes a raw prompt
with no chat template around it and reports <code>prompt_tokens</code> for
exactly the bytes sent, plus one BOS token. Subtract the one and the count
is exact.</p>
<p>That measurement is <strong>a real request</strong>: the text goes to
DeepSeek and is billed as input, the same as sending it would have been.
The cost prints on stderr every time. <code>--offline</code> uses
DeepSeek's published character ratios instead &ndash; free, and an upper
bound that says so.</p>

<h2 id="docs">docs</h2>
<p>DeepSeek's own API documentation, compiled into the binary.</p>
<pre><code>ds docs                          # every page
ds docs search "context cache"
ds docs show guides/thinking_mode
ds docs ask "does FIM support thinking?"
ds docs changelog                # releases, newest first
ds docs sync                     # refresh from the mirror</code></pre>
<p>67 pages, about 85KB compressed: every page of
<a href="{{docs}}">api-docs.deepseek.com</a> plus the FAQ, which lives
outside that site as a JSON blob inside a JavaScript bundle and is not
otherwise readable as text.</p>
<p>Only <code>ask</code> costs anything. It selects pages locally, sends
them whole, and instructs the model to answer from them and cite the page,
so a claim can be checked against a URL. Every command here prints how old
the snapshot is, and every page keeps the upstream URL it came from.</p>

<h2 id="status">status</h2>
<pre><code>ds status</code></pre>
<p>Is the API reachable right now, with this key, from this machine. Two
calls that generate no tokens, so it is free and safe to run in a loop.
That is a different question from
<a href="https://status.deepseek.com/">DeepSeek's incident page</a>, which
reports outages affecting everyone &ndash; a working API behind a broken
proxy looks fine there and broken here.</p>

<h2 id="models">models</h2>
<pre><code>ds models</code></pre>
<p>The API's model list joined with the published rate card, so the price is on
screen next to the model you are about to pick. <code>--json</code> returns the
API's list alone.</p>

<h2 id="balance">balance</h2>
<pre><code>ds balance</code></pre>
<p>Lists every currency the account holds &ndash; a real account returns both a
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
<tr><td class="num">0</td><td>success</td><td>&ndash;</td></tr>
<tr><td class="num">1</td><td>error</td><td>Read stderr</td></tr>
<tr><td class="num">2</td><td>auth</td><td>Key missing or rejected. Do not retry</td></tr>
<tr><td class="num">3</td><td>no balance</td><td>Top up. Do not retry</td></tr>
<tr><td class="num">4</td><td>rate limited</td><td>Back off, then retry</td></tr>
<tr><td class="num">130</td><td>interrupted</td><td>&ndash;</td></tr>
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
    title="The four DeepSeek API formats, compared: deepseek-cli",
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
<p class="lede">DeepSeek exposes the same two models &ndash;
<code>deepseek-v4-flash</code> and <code>deepseek-v4-pro</code> &ndash; through
four different request shapes, so that existing ecosystems can point at it
without code changes. They are not interchangeable.</p>

<h2 id="which">Which one to use</h2>
<div class="tablewrap">
<table>
<thead><tr><th>Format</th><th>Path</th><th>Reach for it when</th></tr></thead>
<tbody>
<tr><td><strong>OpenAI chat</strong><br><code>ds chat</code></td><td><code>/chat/completions</code></td><td>Default. Widest tool support, and the only format with chat prefix completion.</td></tr>
<tr><td><strong>Anthropic Messages</strong><br><code>ds anthropic</code></td><td><code>/anthropic/v1/messages</code></td><td>The surrounding tooling speaks Anthropic &ndash; Claude Code, the Anthropic SDKs, the Claude desktop app.</td></tr>
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
<tr><th>Effort control</th><td><code>reasoning_effort</code></td><td><code>output_config.effort</code></td><td><code>reasoning.effort</code></td><td>&ndash;</td></tr>
<tr><th>JSON Schema output</th><td>&ndash;</td><td>&ndash;</td><td><strong>yes</strong></td><td>&ndash;</td></tr>
<tr><th>Server-side web search</th><td>&ndash;</td><td>&ndash;</td><td><strong>yes</strong></td><td>&ndash;</td></tr>
<tr><th>Prefix completion</th><td><strong>yes</strong> (beta path)</td><td>&ndash;</td><td>&ndash;</td><td>&ndash;</td></tr>
<tr><th>Models</th><td>both</td><td>both</td><td>both</td><td>both</td></tr>
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
<li>If it did not, the field is ignored server-side &ndash; so replaying it
spends input tokens on text the model discards.</li>
</ul>
<p>Sessions in this CLI keep the reasoning stored either way, and decide per
request whether to put it on the wire.</p>

<h2 id="limits">Shared limits</h2>
<ul>
<li><strong>Text only.</strong> Image, document and search-result blocks are
rejected or replaced with placeholder text in every format.</li>
<li><strong>Thinking is on by default</strong>, and what its template costs
depends on <code>--effort</code>: +79 input tokens on flash at the default,
+92 at <code>max</code>, and <strong>nothing at all</strong> at
<code>low</code> &ndash; where the model still reasons.
<a href="{{root}}cost/#thinking">The measured table</a>.</li>
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
    title="What DeepSeek actually costs: cache math and a local usage ledger",
    description="DeepSeek's context cache makes a cached input token 50x cheaper than an uncached one. How the deepseek CLI prices every call, what the local usage ledger records, and the caveats on every figure it prints.",
    keywords="deepseek pricing, deepseek api cost, deepseek context cache, deepseek cache hit tokens, deepseek token cost calculator, deepseek v4 flash price, deepseek usage tracking",
    jsonld=tech_article("What DeepSeek actually costs", "Pricing, context-cache savings, and the deepseek CLI usage ledger.", "cost/"),
    body="""
<h1>Cost</h1>
<p class="lede">DeepSeek's headline feature is a disk-backed context cache that
makes a repeated prompt prefix roughly fifty times cheaper. That saving is
invisible unless something is counting &ndash; so this counts.</p>

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
&ndash; same system prompt, same files, in the same order &ndash; and let the
variable part come last.</p>

<h2 id="thinking">The thinking surcharge</h2>
<p>Thinking mode is on by default and adds a fixed template to your input
before generating a single reasoning token. The size of that template is
<strong>constant regardless of prompt length</strong> &ndash; but it is not
the same at every effort level, and at the low levels it is not there at
all while the model still reasons.</p>

<div class="note">
<span class="tag">measured against the live API, 2026-08-05</span>
<p>Two prompts, 10 and 36 tokens, every level run twice. The surcharge is
exactly constant: 89&minus;10 = 115&minus;36 = 79.</p>
</div>

<div class="tablewrap">
<table>
<thead><tr><th><code>--effort</code></th><th class="num">flash</th><th class="num">pro</th><th>thinking</th></tr></thead>
<tbody>
<tr><td><code>none</code></td><td class="num">+0</td><td class="num">+0</td><td>off entirely</td></tr>
<tr><td><code>minimal</code>, <code>low</code></td><td class="num"><strong>+0</strong></td><td class="num"><strong>+0</strong></td><td>on</td></tr>
<tr><td><code>medium</code>, <code>high</code>, <code>xhigh</code></td><td class="num">+79</td><td class="num">+0</td><td>on</td></tr>
<tr><td><code>max</code></td><td class="num">+92</td><td class="num">+79</td><td>on</td></tr>
</tbody>
</table>
</div>

<p>Two of those levels are in no DeepSeek documentation at all.
<code>none</code> is documented only for the Responses API, but the chat
endpoint takes it and it disables thinking exactly as
<code>--think off</code> does. <code>minimal</code> is undocumented
everywhere. The API rejects only genuinely unknown values, with
<code>unknown variant</code>, which is how this list was established.</p>

<p>The practical consequence: on flash, <code>--effort low</code> removes
the entire input surcharge and <em>keeps</em> the chain of thought. On a
short factual lookup at the default effort, that template is most of the
bill.</p>

<pre><code>ds tokens "your prompt here"           # what it costs at default effort
ds tokens "your prompt here" -e low    # what it costs without the template</code></pre>

<h2 id="ledger">The ledger</h2>
<p>Every call prints one line to stderr and appends one row to
<code>~/.local/state/deepseek/usage.jsonl</code>:</p>
<pre><code>{"ts":"2026-08-05T05:18:12Z","api":"chat","model":"deepseek-v4-flash",
 "in":3242,"cache_hit":3200,"cache_miss":42,"out":1,
 "cost_usd":0.0000109,"saved_usd":0.000439,"ms":1041}</code></pre>
<p>Token counts are exact and are what gets stored; the cost field is a
convenience. That is deliberate &ndash; when DeepSeek changes the rate card,
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
<em>would</em> have cost at the miss rate, minus what they did cost &ndash;
which is the number that tells you whether prompt structuring is paying off.</p>

<h2 id="caveats">What these numbers are not</h2>
<div class="note warn">
<span class="tag">read this before quoting a figure</span>
<ul>
<li><strong>Estimates, not invoices.</strong> Computed from the published USD
rate card. Your account may bill in another currency &ndash;
<code>ds balance</code> shows which.</li>
<li><strong>Peak pricing is not applied.</strong> DeepSeek has announced a 2&times;
multiplier for 09:00&ndash;12:00 and 14:00&ndash;18:00 Beijing time, with no
effective date. Applying it now would double every estimate on a guess, so it
is deliberately left out until the date is announced.</li>
<li><strong>A broader repricing is coming.</strong> On 2026-08-06 DeepSeek
gave notice in the platform console that all API services will be repriced
soon, with a substantial rise expected and no numbers yet. Until there is a
new published card, estimates stay on the card above &ndash; details on the
<a href="{{root}}news/">news page</a>.</li>
<li><strong>Local only.</strong> The ledger records calls made by this CLI on
this machine. It knows nothing about your other clients.</li>
</ul>
</div>
<p><code>--no-ledger</code> skips the write, <code>--no-stats</code> hides the
line, and neither ever fails the command that produced it &ndash; you asked for
a completion, not for bookkeeping.</p>
""",
))

PAGES.append(dict(
    slug="bench/",
    crumb="bench",
    title="DeepSeek V4-Pro benchmarks vs GPT, Claude, Kimi and GLM, and the kill line",
    description="How deepseek-v4-pro (GA 0813) and v4-flash score against Kimi K3, GLM-5.2, Claude Opus 4.8 and Fable 5 on the agent suites, what the GA checkpoint changed, and the economics idea behind the DeepSeek kill line.",
    keywords="deepseek v4 pro benchmarks, deepseek v4 pro vs claude, deepseek vs gpt-5.6, deepseek vs kimi k3, deepseek vs glm, deepseek 斩杀线, deepseek kill line, deepseek v4 pro 0813, deepseek agent benchmarks, terminal bench deepseek, deepswe, toolathlon",
    jsonld=faq([
        ("How does DeepSeek-V4-Pro score against other models on agent benchmarks?",
         "On DeepSeek's own launch-day chart (2026-08-12), V4-Pro-0813 scores Terminal-Bench 2.1 87.9, DeepSWE 62.7, Toolathlon-Verified 74.1, CyberGym 83.3, HLE-with-tools 60.0 and AutomationBench 31.8. It sits in the same cluster as Kimi K3, Claude Fable 5 and Opus 4.8: within a point of Fable 5 on Terminal-Bench (88.0) and CyberGym (83.1), ahead of Opus 4.8 on several execution suites, but behind Kimi K3 on Terminal-Bench, DeepSWE, Toolathlon and DSBench-Hard. These are vendor numbers from one harness and are not yet independently reproduced."),
        ("What did the V4-Pro GA (0813) checkpoint change over the preview?",
         "The model ID and the rate card did not change; the checkpoint did. Against the April V4-Pro preview, DeepSeek's chart shows large agentic gains: DeepSWE 12.8 to 62.7, DSBench-Hard 31.1 to 67.2, CyberGym 52.7 to 83.3, Terminal-Bench 2.1 72.1 to 87.9, Toolathlon 55.9 to 74.1. Jumps that size point to agent post-training and better tool-error handling rather than a new base model, and none of them are independently verified yet."),
        ("What is the DeepSeek kill line (斩杀线)?",
         "The kill line is a community idea that DeepSeek's price-to-capability ratio sets a threshold that removes the reason to exist for any model that is both weaker and more expensive. V4-Pro is roughly 11x cheaper than GPT-5.6 Sol on cache-miss input and 34x cheaper on output, and about 138x cheaper on cache-hit input. It does not kill the frontier: the strongest closed models still finish the hardest tasks in fewer turns. It kills the middle, where a model costs more and does less."),
        ("Is DeepSeek-V4-Pro better than Claude or GPT for coding agents?",
         "Per attempt, the strongest closed models remain more reliable on the hardest multi-step tasks and usually need less steering. Per dollar, V4-Pro changes the arithmetic: its cheap cached input makes repeated review, parallel workers and long tool loops affordable in a way per-token-stronger models are not. The practical answer is to route by role, run an internal bake-off, and measure successful-task cost, not per-token price."),
    ]),
    body="""
<h1>Benchmarks</h1>
<p class="lede">Where <code>deepseek-v4-pro</code> and <code>deepseek-v4-flash</code>
sit against the field, what the 0813 checkpoint changed, and the economics
argument the Chinese community calls the <span lang="zh">斩杀线</span>, the
kill line. The numbers below are DeepSeek's own launch-day figures unless
marked otherwise; read the caveats first.</p>

<div class="note warn">
<span class="tag">read this before quoting a number</span>
<ul>
<li><strong>Vendor numbers, one harness.</strong> The table is DeepSeek's own
agent-benchmark chart, published 2026-08-12 with the GA. A score is a
model-and-harness result, not a model-only one, and no independent same-harness
run of 0813 exists yet. Treat these as the claim, not the verdict.</li>
<li><strong>No GPT-5.6 column.</strong> DeepSeek's chart compares against Kimi
K3, GLM-5.2, Claude Opus 4.8 and Fable 5 only. GPT figures elsewhere on this
page are drawn from those vendors' own releases and are cross-vendor, so they
are looser still.</li>
<li><strong>Kimi and GLM are single-sourced.</strong> Those two columns appear
only on the extended variant of the chart; the shared columns are identical
across every copy, so the numbers are consistent, but the Kimi and GLM rows
rest on one source.</li>
<li><strong>Independent history says be careful.</strong> The one held-out
check on the V4-Pro <em>preview</em>, from NIST/CAISI, put it closer to GPT-5
and roughly eight months behind the frontier, below its self-reported
position. No equivalent 0813 evaluation exists yet.</li>
</ul>
</div>

<h2 id="table">The launch table</h2>
<p>Higher is better. HLE is shown as without-tools / with-tools. Every figure
is from DeepSeek's GA chart of 2026-08-12; a dash means the vendor did not
report it.</p>
<div class="tablewrap">
<table>
<thead><tr>
<th>Benchmark</th>
<th class="num">V4-Pro 0813</th>
<th class="num">V4-Flash 0731</th>
<th class="num">Kimi K3</th>
<th class="num">GLM-5.2</th>
<th class="num">Opus 4.8</th>
<th class="num">Fable 5</th>
</tr></thead>
<tbody>
<tr><td>Terminal-Bench 2.1</td><td class="num">87.9</td><td class="num">82.7</td><td class="num">88.3</td><td class="num">81.6</td><td class="num">85.0</td><td class="num">88.0</td></tr>
<tr><td>DeepSWE</td><td class="num">62.7</td><td class="num">54.4</td><td class="num">67.5</td><td class="num">46.2</td><td class="num">58.0</td><td class="num">70.0</td></tr>
<tr><td>Toolathlon-Verified</td><td class="num">74.1</td><td class="num">70.3</td><td class="num">76.5</td><td class="num">59.9</td><td class="num">76.2</td><td class="num">77.9</td></tr>
<tr><td>CyberGym</td><td class="num">83.3</td><td class="num">76.7</td><td class="num">80.0</td><td class="num">&ndash;</td><td class="num">78.3</td><td class="num">83.1</td></tr>
<tr><td>NL2Repo</td><td class="num">61.5</td><td class="num">54.2</td><td class="num">&ndash;</td><td class="num">48.9</td><td class="num">69.7</td><td class="num">&ndash;</td></tr>
<tr><td>AutomationBench</td><td class="num">31.8</td><td class="num">25.1</td><td class="num">30.8</td><td class="num">12.9</td><td class="num">27.2</td><td class="num">29.1</td></tr>
<tr><td>DSBench-FullStack</td><td class="num">71.1</td><td class="num">68.7</td><td class="num">63.0</td><td class="num">51.8</td><td class="num">71.6</td><td class="num">77.2</td></tr>
<tr><td>DSBench-Hard</td><td class="num">67.2</td><td class="num">59.6</td><td class="num">73.7</td><td class="num">54.5</td><td class="num">71.7</td><td class="num">68.3</td></tr>
<tr><td>Agents' Last Exam</td><td class="num">25.7</td><td class="num">25.2</td><td class="num">24.5</td><td class="num">23.9</td><td class="num">25.7</td><td class="num">&ndash;</td></tr>
<tr><td>HLE (no / tools)</td><td class="num">42.7/60.0</td><td class="num">37.8/51.5</td><td class="num">43.5/56.0</td><td class="num">40.5/54.7</td><td class="num">49.8/57.9</td><td class="num">53.3/63.0</td></tr>
</tbody>
</table>
</div>
<p>The honest read of the row-by-row: V4-Pro is <strong>within a point of Fable
5</strong> on Terminal-Bench and CyberGym, <strong>ahead of Opus 4.8</strong> on
Terminal-Bench, DeepSWE, CyberGym and AutomationBench, and <strong>behind Kimi
K3</strong> on Terminal-Bench, DeepSWE, Toolathlon and DSBench-Hard. It is in
the cluster, not clear of it. On knowledge without tools (HLE) the closed
models still lead. Flash trails Pro across the board but stays remarkably close
for a fifth of the price, which is the whole point of the next two sections.</p>

<h2 id="delta">What GA changed</h2>
<p>The model ID stayed <code>deepseek-v4-pro</code> and the
<a href="{{root}}cost/">rate card</a> did not move. What moved is the
checkpoint. Against the April preview, DeepSeek's own chart shows the gains
landing almost entirely in the agentic and SWE suites:</p>
<div class="tablewrap">
<table>
<thead><tr><th>Benchmark</th><th class="num">Preview</th><th class="num">GA 0813</th><th class="num">Delta</th></tr></thead>
<tbody>
<tr><td>DeepSWE</td><td class="num">12.8</td><td class="num">62.7</td><td class="num">+49.9</td></tr>
<tr><td>DSBench-Hard</td><td class="num">31.1</td><td class="num">67.2</td><td class="num">+36.1</td></tr>
<tr><td>CyberGym</td><td class="num">52.7</td><td class="num">83.3</td><td class="num">+30.6</td></tr>
<tr><td>DSBench-FullStack</td><td class="num">41.8</td><td class="num">71.1</td><td class="num">+29.3</td></tr>
<tr><td>NL2Repo</td><td class="num">38.5</td><td class="num">61.5</td><td class="num">+23.0</td></tr>
<tr><td>Toolathlon-Verified</td><td class="num">55.9</td><td class="num">74.1</td><td class="num">+18.2</td></tr>
<tr><td>Terminal-Bench 2.1</td><td class="num">72.1</td><td class="num">87.9</td><td class="num">+15.8</td></tr>
</tbody>
</table>
</div>
<p>A near five-fold jump on DeepSWE is not a new base model. Gains shaped like
this come from agent post-training: better tool-error recovery, better context
policy, reinforcement on the harness the benchmark runs in. That is real and it
is useful, and it is also exactly the kind of gain that can be harness-specific,
which is why the caveats above matter and why the preview numbers are the floor,
not these.</p>

<h2 id="kill-line">The kill line (<span lang="zh">斩杀线</span>)</h2>
<p>The term comes from Chinese gaming: the <span lang="zh">斩杀线</span> is the
health threshold below which a target can be executed outright. Applied to
models, the idea is that DeepSeek's price-to-capability ratio draws a line, and
any model that is <em>both weaker and more expensive</em> falls below it and has
no reason to be chosen. The lever is price, and the gap is not small:</p>
<div class="tablewrap">
<table>
<thead><tr><th>Per 1M tokens</th><th class="num">v4-flash</th><th class="num">v4-pro</th><th class="num">GPT-5.6 Sol</th><th class="num">pro is cheaper by</th></tr></thead>
<tbody>
<tr><td>input, cache miss</td><td class="num">$0.14</td><td class="num">$0.435</td><td class="num">$5.00</td><td class="num">~11.5x</td></tr>
<tr><td>input, cache hit</td><td class="num">$0.0028</td><td class="num">$0.003625</td><td class="num">$0.50</td><td class="num">~138x</td></tr>
<tr><td>output</td><td class="num">$0.28</td><td class="num">$0.87</td><td class="num">$30.00</td><td class="num">~34.5x</td></tr>
</tbody>
</table>
</div>
<p class="small">DeepSeek prices are the published USD rate card of
2026-08-02, unchanged at GA; they are a conversion of the RMB card
(&yen;3 / &yen;0.025 / &yen;6 per 1M for pro) at one consistent rate. GPT-5.6
Sol prices are from OpenAI's own listing. A <a href="{{root}}news/">broad
DeepSeek repricing</a> is announced but has no date, so it is not applied here.</p>
<p>The sober version matters as much as the slogan. The kill line is real for
the <em>middle</em> of the market: a model that costs more than V4-Pro and
scores below it on the table above is hard to justify, and that is most of the
field. It is <strong>not</strong> real for the frontier. On the hardest
multi-step work the strongest closed models still finish in fewer turns and
need less steering, and per-attempt reliability is a thing you can measure in
wall-clock and interventions, not just in dollars. DeepSeek does not have to win
per attempt to win per dollar, and it does not have to win per dollar to lose
the one task where getting it right the first time is the whole job.</p>

<h2 id="practice">What it means in practice</h2>
<p>The economics only pay off if the workflow is built for them. Three moves,
each of which this CLI is shaped to support:</p>
<ul>
<li><strong>Route by role.</strong> Use <code>deepseek-v4-pro</code> for
planning, ambiguous changes, security review and recovery; let
<code>deepseek-v4-flash</code> do bounded implementations and parallel work.
<code>ds chat -m deepseek-v4-pro</code> and the
<a href="{{root}}formats/">Anthropic remap</a> make the switch one flag.</li>
<li><strong>Structure prompts for the cache.</strong> A cached input token
costs about 1/50th of an uncached one. Keep the system prompt, tool schemas,
repository map and durable instructions in an identical prefix and put the
volatile part last; <code>ds usage</code> reports what the cache saved so you
can see whether it is working. This is where the 138x cache-hit number turns
from a table cell into a bill.</li>
<li><strong>Measure successful-task cost, not per-token price.</strong> A
cheaper model that retries five times can cost more than a dearer one that lands
first. The <a href="{{root}}cost/#ledger">ledger</a> stores exact token counts
per call, so you can price a whole task under any rate card, including the one
that has not been announced yet.</li>
</ul>

<h2 id="sources">Sources</h2>
<p>The launch table and the preview deltas are transcribed from DeepSeek's
official agent-benchmark chart, published on the
<a href="{{docs}}/quick_start/pricing">Models &amp; Pricing</a> page and
circulated on 2026-08-12; the extended variant carrying the Kimi K3 and GLM-5.2
columns was the widest copy available. Rate-card figures are DeepSeek's own and
OpenAI's own. The independent-evaluation caveat refers to the NIST/CAISI review
of the V4-Pro preview. Numbers change fast and vendor charts are vendor charts;
cross-check a live leaderboard before betting on a single cell.</p>
""",
))

PAGES.append(dict(
    slug="news/",
    crumb="news",
    title="DeepSeek API news: V4-Pro GA, the announced price rise, releases",
    description="What is changing in the DeepSeek API: V4-Pro's official release (DeepSeek-V4-Pro-0813), an across-the-board price increase announced with no date yet, the 2x peak-hour pricing policy, V4-Flash's official release, and what each one does to the cost of a call.",
    keywords="deepseek v4 pro release, deepseek v4 pro ga, deepseek-v4-pro-0813, deepseek api price increase, deepseek price rise 2026, deepseek peak hour pricing, deepseek api news, deepseek api changelog, deepseek v4 flash release, deepseek pricing change",
    jsonld=faq([
        ("Is DeepSeek V4-Pro officially released?",
         "Yes. On 2026-08-12 the model version on DeepSeek's Models & Pricing page changed to DeepSeek-V4-Pro-0813, ending the preview that had run since 2026-04-24. The model ID is unchanged (deepseek-v4-pro), the rate card is unchanged, and the release focuses on agentic post-training rather than new pretraining."),
        ("Is DeepSeek raising its API prices?",
         "Yes, a rise is announced but not yet in effect. On 2026-08-06 (Beijing time) DeepSeek posted a notice in the platform console and emailed API account holders saying all API services will be repriced in the near term and that the increase is expected to be substantial, advising developers to plan call volume and top-ups accordingly. No new rate card and no effective date have been published. Separately, a 2x peak-hour pricing policy has been announced since June 2026, also without an effective date."),
        ("When does DeepSeek's peak-hour pricing start?",
         "No effective date has been announced. The published policy: during peak hours, 09:00-12:00 and 14:00-18:00 Beijing time (01:00-04:00 and 06:00-10:00 UTC) daily, all billing items cost 2x the regular price. Until DeepSeek announces the date, the policy is not active and estimates should not apply it."),
        ("What are DeepSeek's current API prices?",
         "Per 1M tokens, from the rate card published 2026-08-02: deepseek-v4-flash is $0.14 input on a cache miss, $0.0028 on a cache hit, and $0.28 output; deepseek-v4-pro is $0.435 input on a miss, $0.003625 on a hit, and $0.87 output."),
        ("Where can I follow DeepSeek API changes?",
         "DeepSeek's own change log lives at api-docs.deepseek.com/updates. The deepseek CLI carries the same documentation inside the binary: `deepseek docs changelog` prints it offline, and `deepseek docs sync` refreshes the snapshot."),
    ]),
    body="""
<h1>News</h1>
<p class="lede">What is changing in the DeepSeek API, and what it does to the
cost of a call. Curated from official announcements and checked against the
live API where that is possible; the in-terminal feed is
<code>ds docs changelog</code>.</p>

<h2 id="no-rollback">2026-08-13 &middot; No, V4-Pro did not roll back</h2>
<p>Ask <code>deepseek-v4-pro</code> what model it is and it may tell you it is
GPT-4o. A model that answers with a competitor's name looks like a swap, and
the question came up: did the 0813 GA get pulled and replaced with something
older? Short answer, no. The build behind <code>deepseek-v4-pro</code> is still
<strong>DeepSeek-V4-Pro-0813</strong>.</p>
<figure class="shot">
<a href="deepseek-v4-pro-0813-still-live-2026-08-13.jpg">
<img src="deepseek-v4-pro-0813-still-live-2026-08-13.jpg"
     alt="DeepSeek's Models &amp; Pricing docs page captured on 2026-08-13: the Model Details table still lists MODEL VERSION DeepSeek-V4-Flash-0731 and DeepSeek-V4-Pro-0813, with 1M context and 384K max output unchanged."
     width="1400" height="1000" loading="lazy"></a>
<figcaption>The Models &amp; Pricing page on 2026-08-13. The version cell still
reads DeepSeek-V4-Pro-0813, and the page has not been edited since it changed
on 2026-08-12. Click for full size.</figcaption>
</figure>
<p>The record is consistent on every side that can actually be checked. The
<a href="{{docs}}/quick_start/pricing">Models &amp; Pricing</a> version cell
still reads DeepSeek-V4-Pro-0813 (the page's last-modified date is 2026-08-12,
the GA day, and it has not moved since). The API still serves
<code>deepseek-v4-pro</code>. Third-party hosts that mirror the build, OpenRouter
and NanoGPT among them, list 0813. The change log carries no rollback. Nothing
was withdrawn.</p>
<p>So why does it say GPT-4o? Because a language model does not know its own
name or its training date. Asked to state its version with nothing to look at,
<code>deepseek-v4-pro</code> answers differently on different samples:</p>
<div class="term">
<div class="term-bar"><span class="dot r"></span><span class="dot y"></span><span class="dot g"></span><span class="title">deepseek-v4-pro: "state only your model name and version"</span></div>
<pre><code>GPT-4o
GPT-4o
DeepSeek-V3-0324
<span class="c"># and flash, asked the same, answered: Qwen/Qwen2.5-7B-Instruct</span></code></pre>
</div>
<p>That is the model repeating identities from its training data, not reporting
what it was deployed as. It is not a version signal and never was, on any
model. The signal that is real is the one nobody has to imagine: the docs
version cell and the model list. <code>ds docs show quick_start/pricing</code>
prints that same table offline, and <code>ds models</code> lists what the
endpoint serves. Neither asks the model to introspect, which is exactly why the
CLI carries the documentation inside the binary: the answer to &ldquo;what am I
calling&rdquo; should not depend on the model's memory of itself.</p>

<h2 id="v4-pro">2026-08-12 &middot; V4-Pro official release (0813)</h2>
<p>The preview is over. The
<a href="{{docs}}/quick_start/pricing">Models &amp; Pricing page</a> now lists
the model version as <strong>DeepSeek-V4-Pro-0813</strong> &ndash; a quiet
table-cell change, no news post upstream. The model ID stays
<code>deepseek-v4-pro</code>, the specs stay 1M context and 384K max output,
and the rate card stays where it has been since 2026-08-02.</p>
<figure class="shot">
<a href="deepseek-v4-pro-0813-models-pricing-2026-08-12.jpg">
<img src="deepseek-v4-pro-0813-models-pricing-2026-08-12.jpg"
     alt="DeepSeek's Models &amp; Pricing docs page: in the Model Details table, the MODEL VERSION cell for deepseek-v4-pro reads DeepSeek-V4-Pro-0813, circled in red, next to DeepSeek-V4-Flash-0731; the surrounding rows still show 1M context length and 384K max output."
     width="1600" height="1473" loading="lazy"></a>
<figcaption>The Models &amp; Pricing page as captured on 2026-08-12 &ndash;
the circled version cell is the entire announcement, and the primary source
for this entry. Click for full size.</figcaption>
</figure>
<p>What changed is the checkpoint. Like Flash's 0731 release, the GA build is
re-post-trained for agent work; DeepSeek's launch-day numbers circulating in
the community put Terminal-Bench 2.1 at ~87.9 (preview: 72.1), DeepSWE at
~62.7 (preview: 12.8) and Toolathlon-Verified at ~74.1 (preview: 55.9).
Jumps that size are post-training and harness work, not a new base model
&ndash; and none of them are independently verified yet, so treat the
preview numbers as the floor and these as the claim.</p>
<p><strong>What it changes here: nothing to do.</strong> Anything already
sending <code>deepseek-v4-pro</code> &ndash; <code>ds chat -m
deepseek-v4-pro</code>, the <code>claude-opus-*</code> remap on the
<a href="{{root}}formats/">Anthropic format</a> &ndash; has been on the new
checkpoint since the cell changed. Same price, stronger model; the
<a href="{{root}}cost/">estimates</a> already price it correctly.</p>

<h2 id="price-rise">2026-08-06 &middot; a broad price rise is coming<span class="chip warn">date tba</span></h2>
<p>DeepSeek posted a notice in the
<a href="https://platform.deepseek.com/">platform console</a>: all API
services will be repriced &ldquo;in the near term&rdquo;, and the increase is
expected to be substantial. Developers are advised to plan call volume and
keep top-ups sized to what they will actually use. The final schedule and the
new numbers are &ldquo;subject to the official announcement&rdquo; &ndash; and
as of this page's build there is none.</p>
<p>The notice appears in the console rather than on the docs site, so it is
easy to miss from code. It was
<a href="https://finance.sina.com.cn/tech/roll/2026-08-06/doc-inimivft0773504.shtml">widely
reported</a> on 2026-08-06 Beijing time.</p>
<p>The same notice went out by email to API account holders the same day,
bilingual and unambiguous about the direction. The email adds one thing
the console banner does not: continuing to use the service after the
adjustment counts as accepting it, and the offered alternative is
cancelling and applying for a refund. Received and archived here as the
primary source:</p>
<figure class="shot">
<a href="deepseek-api-billing-adjustment-email-2026-08-06.jpg">
<img src="deepseek-api-billing-adjustment-email-2026-08-06.jpg"
     alt="DeepSeek's bilingual email, subject 'DeepSeek API Billing Adjustment Announcement': the overall pricing for DeepSeek API services will rise in the near future with a significant increase expected, the specific plan subject to official notice; continued use after the adjustment counts as acceptance, otherwise users may cancel and apply for a refund."
     width="1840" height="2470" loading="lazy"></a>
<figcaption>DeepSeek's billing-adjustment announcement as emailed to API
users on 2026-08-06 &ndash; click for full size.</figcaption>
</figure>
<p><strong>What it changes here: nothing, yet.</strong> The
<a href="{{root}}cost/">cost page</a> and <code>ds models</code> price from
the published card of 2026-08-02 until DeepSeek publishes a new one. The
<a href="{{root}}cost/#ledger">ledger</a> stores exact token counts rather
than prices, deliberately &ndash; when the card changes, every historical
call can be repriced under it.</p>

<h2 id="peak-pricing">announced 2026-06-29 &middot; 2&times; during peak hours<span class="chip warn">date tba</span></h2>
<p>The pricing page has carried this since late June: the API will move to
peak/off-peak pricing, with every billing item &ndash; input, cached input,
output &ndash; costing <strong>2&times; the regular price during peak
hours</strong>. Off-peak, the current card stands.</p>
<div class="tablewrap">
<table>
<thead><tr><th>Window</th><th>Beijing (UTC+8)</th><th>UTC</th><th class="num">Multiplier</th></tr></thead>
<tbody>
<tr><td>peak</td><td>09:00&ndash;12:00</td><td>01:00&ndash;04:00</td><td class="num">2&times;</td></tr>
<tr><td>peak</td><td>14:00&ndash;18:00</td><td>06:00&ndash;10:00</td><td class="num">2&times;</td></tr>
<tr><td>off-peak</td><td>everything else</td><td>everything else</td><td class="num">1&times;</td></tr>
</tbody>
</table>
</div>
<p>The effective date is still &ldquo;subject to the official
announcement&rdquo;. This CLI deliberately does not apply the multiplier to
its estimates before that date exists &ndash; doubling every figure on a
guess would be inventing data. The day it is real, the ledger's stored token
counts make the switch a repricing, not a migration.</p>
<p>Two practical notes. First, the <a href="{{root}}cost/#cache">context
cache</a> discount is 50&times;; the peak multiplier is 2&times;. Prompt
structure will still dominate your bill. Second, if batch work can move,
move it &ndash; the off-peak window covers the whole European and American
working day.</p>

<h2 id="v4-flash">2026-07-31 &middot; V4-Flash official release</h2>
<p>The official DeepSeek-V4-Flash API entered public beta: same model name,
same calling convention, re-post-trained weights with substantially stronger
agent behaviour &ndash; DeepSeek's published numbers have it ahead of
V4-Pro-Preview on Terminal Bench, DeepSWE and the rest of the agent suite.
It natively speaks the <a href="{{root}}formats/">Responses format</a> and is
explicitly adapted for Codex. The official V4-Pro release &ldquo;will follow
soon&rdquo;.</p>

<h2 id="v4">2026-04-24 &middot; V4 arrives, the old names leave</h2>
<p><code>deepseek-v4-pro</code> and <code>deepseek-v4-flash</code> became the
API's two models, served through both the OpenAI and Anthropic interfaces.
The legacy names <code>deepseek-chat</code> and <code>deepseek-reasoner</code>
were aliased to flash for a grace period and retired on
<strong>2026-07-24</strong> &ndash; anything still sending them gets an
error, not a quiet remap.</p>

<h2 id="watch">Watching this without watching this page</h2>
<p>This page is curated, not generated, so it carries what matters and skips
what does not. The complete feed:</p>
<pre><code>ds docs changelog     # DeepSeek's own change log, in the terminal, offline
ds docs sync          # refresh the snapshot the binary carries
ds models             # the rate card the estimates use, next to the live model list</code></pre>
<p>Upstream: the official <a href="{{docs}}/updates">change log</a> and
<a href="{{docs}}/quick_start/pricing">pricing page</a>, and
<a href="https://status.deepseek.com/">status.deepseek.com</a> for incidents.</p>
""",
))

PAGES.append(dict(
    slug="agents/",
    crumb="agents",
    title="deepseek-cli for agents and scripts: the output contract",
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
<p>Cost deliberately does not appear there &ndash; it would have meant wrapping
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
model are never retried &ndash; a second call would be billed twice.</p>

<h2 id="preflight">Preflight</h2>
<pre><code>ds check --json
# {"base_url":"...","key_set":true,"probes":[...],"ok":true}</code></pre>
<p>One command, six endpoints, a fraction of a cent. If <code>ok</code> is
false, read <code>probes[].error</code>.</p>

<h2 id="errors">Errors say what to do</h2>
<pre><code>$ ds chat hi
Error: Insufficient Balance (HTTP 402)
  out of balance: deepseek balance &ndash; top up at https://platform.deepseek.com/top_up</code></pre>

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
<li>Do not retry on exit 2 or 3 &ndash; bring them to the human.</li>
<li>The API is text-only: no images, no documents.</li>
<li>Slow starts of up to ten minutes are normal under load, not a failure.</li>
</ul>
""",
))


PLAYGROUND_BODY = """
<h1>playground</h1>
<p class="lede">The DeepSeek API in a browser, with no API key. Enrolling costs
about a second of CPU and nothing else &ndash; no account, no email, no card.</p>

<div id="pg-enrol" class="pg-enrol">
  <p><strong>What this is.</strong> A gateway run by this project holds a real
  DeepSeek API key and relays your requests, metered and capped. To stop one
  person draining it for everyone, enrolling asks your browser to solve a small
  proof-of-work puzzle. That puzzle is the whole signup.</p>
  <ul class="pg-terms">
    <li><span>model</span> deepseek-v4-flash</li>
    <li><span>per day</span> 30 requests &middot; 60k input &middot; 20k output tokens</li>
    <li><span>privacy</span> prompts are relayed to DeepSeek and are not stored or
        logged by the gateway; only token counts and cost are recorded</li>
    <li><span>when it runs out</span> the quota resets at 00:00 UTC, and the
        <a href="{{root}}install/">CLI with your own key</a> has no limits at all</li>
  </ul>
  <button id="pg-enrolBtn" class="pg-primary" type="button">Enrol this browser</button>
{{turnstile_widget}}  <p id="pg-enrolStatus" class="pg-status" hidden></p>
  <noscript><p class="pg-status">This page needs JavaScript. The
  <a href="{{root}}install/">command-line tool</a> does not:
  <code>deepseek free</code> does the same thing in a shell.</p></noscript>
</div>

<div id="pg-app" class="pg-app" hidden>
  <div class="pg-main">
    <div id="pg-log" class="pg-log term" aria-live="polite" aria-label="Conversation"></div>
    <div id="pg-error" class="pg-error" role="alert" hidden></div>

    <div id="pg-composer" class="pg-composer">
      <div id="pg-chatFields">
        <label class="pg-sr" for="pg-prompt">Message</label>
        <textarea id="pg-prompt" rows="3" placeholder="why is the sky blue&#10;&#10;Enter to send, Shift+Enter for a newline"></textarea>
      </div>
      <div id="pg-fimFields" hidden>
        <label class="pg-sr" for="pg-suffix">Suffix</label>
        <input id="pg-suffix" type="text" placeholder="text after the gap, e.g.     return c">
      </div>
      <div class="pg-actions">
        <button id="pg-send" class="pg-primary" type="button">Send</button>
        <button id="pg-stop" type="button" hidden>Stop</button>
        <button id="pg-clear" type="button">Clear</button>
        <span id="pg-usage" class="pg-usage"></span>
      </div>
    </div>
  </div>

  <aside class="pg-side">
    <h2>request</h2>
    <label for="pg-format">format</label>
    <select id="pg-format">
      <option value="chat">chat &ndash; OpenAI</option>
      <option value="anthropic">anthropic &ndash; Messages</option>
      <option value="responses">responses &ndash; OpenAI Responses</option>
      <option value="fim">fim &ndash; fill in the middle</option>
    </select>
    <p id="pg-formatNote" class="pg-note"></p>

    <div id="pg-searchField" hidden>
      <label class="pg-check" for="pg-search">
        <input id="pg-search" type="checkbox">
        web search
      </label>
      <p class="pg-note">DeepSeek searches and reads pages server-side. Costs
      one of the free tier's three daily searches, because the pages it reads
      are billed as input tokens &ndash; about ten ordinary turns' worth.</p>
    </div>

    <label for="pg-think">thinking</label>
    <select id="pg-think">
      <option value="">on (the API's default)</option>
      <option value="off">off</option>
    </select>

    <label for="pg-effort">effort</label>
    <select id="pg-effort">
      <option value="">default</option>
      <option value="minimal">minimal</option>
      <option value="low">low</option>
      <option value="medium">medium</option>
      <option value="high">high</option>
      <option value="max">max</option>
    </select>

    <label for="pg-maxTokens">max tokens</label>
    <input id="pg-maxTokens" type="number" min="1" max="4096" value="1024">

    <label for="pg-temperature">temperature</label>
    <input id="pg-temperature" type="number" min="0" max="2" step="0.1" placeholder="default">

    <label for="pg-system">system prompt</label>
    <textarea id="pg-system" rows="2" placeholder="optional"></textarea>

    <h2>the same thing, in a shell</h2>
    <pre id="pg-command" class="pg-command"></pre>
    <button id="pg-copy" type="button">copy</button>

    <h2>free tier</h2>
    <p id="pg-quota" class="pg-note"></p>
    <label for="pg-gateway">gateway</label>
    <input id="pg-gateway" type="url" spellcheck="false">
    <button id="pg-reset" type="button">Forget this browser's token</button>
  </aside>
</div>

<h2>Why the puzzle</h2>
<p>Because the alternative is a signup form. Every request here spends real
money on a real API key, so something has to stop one script taking the lot.
An account would do it and would also be the thing that stops most people
trying at all &ndash; so instead your browser burns about a second of CPU, once,
and that is the account.</p>
<p>It is not a security boundary and is not pretending to be one. Identities can
be farmed; a daily budget cap cannot be. That cap is what actually protects the
service, which is why the puzzle can stay small enough not to matter to you.
The reasoning is written out in
<a href="{{repo}}/blob/main/gateway/DESIGN.md">gateway/DESIGN.md</a>.</p>

<h2>The command panel is the point</h2>
<p>Whatever you set on the right, the panel shows the <code>deepseek</code>
invocation that does the same thing. Get the request right here, take the line
away with you. Everything on this page works from a terminal, and from a
terminal it also streams to stdout, keeps a usage ledger, and remembers the
conversation.</p>

<pre class="term"><code><span class="c">$</span> go install github.com/thevibeworks/deepseek-cli/cmd/deepseek@latest
<span class="c">$</span> deepseek free
<span class="c">$</span> deepseek chat "why is the sky blue"</code></pre>

<p>The CLI and this page enrol against the same gateway with the same protocol
and get the same quota &ndash; but they are separate implementations of it, in
different languages, pinned to a shared table of test vectors.</p>

<h2>What it will not do</h2>
<ul>
<li><strong>deepseek-v4-pro.</strong> Three times the price. The free tier serves
flash and refuses pro outright rather than quietly downgrading it, because an
answer you attribute to the wrong model is worse than no answer.</li>
<li><strong>Long prompts.</strong> Bodies are capped at 128KB, output at 4K
tokens per call.</li>
<li><strong>Tools.</strong> They are forwarded, but this page does not run them.
Neither does the CLI &ndash; it prints them.</li>
</ul>
<p>All of those limits disappear the moment you use your own key, which costs
about a third of a cent for a thousand ordinary turns. That is the honest pitch:
this exists so you can find out whether the API is worth a key, without needing
one first.</p>
"""

PAGES.append(dict(
    slug="playground/",
    crumb="playground",
    title="DeepSeek playground: try the API with no key",
    description="Use the DeepSeek API in your browser without an API key or a signup. Chat, Anthropic Messages, Responses and FIM formats, streaming, with the equivalent deepseek CLI command shown for every request.",
    keywords="deepseek playground, deepseek api free, deepseek without api key, try deepseek api, deepseek chat online, deepseek api demo, free deepseek api",
    jsonld=tech_article(
        "DeepSeek playground",
        "Try the DeepSeek API in a browser with no API key, and see the equivalent command-line invocation for every request.",
        "playground/",
    ),
    body=PLAYGROUND_BODY.replace(
        "{{turnstile_widget}}",
        (
            '  <div id="pg-turnstile" class="pg-turnstile"'
            f' data-sitekey="{TURNSTILE_SITEKEY}"></div>\n'
        )
        if TURNSTILE_SITEKEY
        else "",
    ),
    scripts=(
        (
            '<script src="https://challenges.cloudflare.com/turnstile/v0/api.js'
            '?render=explicit&amp;onload=dsTurnstileOnload" async defer></script>\n'
            if TURNSTILE_SITEKEY
            else ""
        )
        + '<script src="{{root}}md.js"></script>\n'
        + '<script src="{{root}}playground.js"></script>\n'
    ),
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
        title="Not found: deepseek-cli",
        description="That page does not exist. The deepseek-cli documentation index.",
        keywords="deepseek cli",
        jsonld=SOFTWARE_JSONLD,
        band_sea=True,
        body="""
<h1>404</h1>
<p class="lede">No such page &ndash; deep water. The whole site is eight of them:</p>

<ul>
<li><a href="BASE/">overview</a> &ndash; what it is and why</li>
<li><a href="BASE/install/">install</a> &ndash; binaries, Go, the <code>ds</code> alias, configuration</li>
<li><a href="BASE/commands/">commands</a> &ndash; every command and flag</li>
<li><a href="BASE/formats/">formats</a> &ndash; DeepSeek's four wire formats compared</li>
<li><a href="BASE/cost/">cost</a> &ndash; pricing, the context cache, the usage ledger</li>
<li><a href="BASE/news/">news</a> &ndash; what is changing upstream, including the announced price rise</li>
<li><a href="BASE/agents/">agents</a> &ndash; the scripting contract</li>
<li><a href="BASE/playground/">playground</a> &ndash; the API in a browser, no key needed</li>
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
    notfound = notfound.replace('src="./waves.js"', f'src="{SITE}/waves.js"')
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
