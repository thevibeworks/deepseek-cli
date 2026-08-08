# DESIGN — the material

Read this before touching anything under `site/`. `TASTE.md` holds prior
rulings — the things we tried and rejected, with why. This file holds the
material: the tokens, the laws they follow, and the two gestures the site
is built around.

The test for any change here: **a change that makes the surface prettier
and the task harder must fail.** The task on this site is reading
documentation.

If the material changes, rewrite this file in the same commit. A contract
that lies about the code is worse than no contract.

## Surfaces

| Surface | Class | Primary task | Protected functions |
| --- | --- | --- | --- |
| `/` overview | brand | decide whether to install this | the pitch, the badges, the four buttons, the first terminal transcript |
| `install`, `commands`, `formats`, `cost`, `agents` | product | find one command or one flag and copy it | tables and their third column, `.term` transcripts, inline `code`, anchors, the pager |
| `news` | product | see what changed and when | dates, version numbers, links out |
| `playground` | product | send a real request without a key | the prompt field, Send/Stop, the transcript, error text, quota readout |
| `404` | product | get somewhere real | the links |

Brand moves applied to product surfaces are the failure mode to watch.
Everything below the fold on the overview page is a product surface
wearing the brand page's clothes.

## The material

No framework, no build step for the reader, no JavaScript required to
read anything. The generator (`site/build.py`) is stdlib Python; the page
is one stylesheet and two scripts that only ever upgrade something already
on the page.

| Dimension | Value | Law |
| --- | --- | --- |
| Colour | one token block on `:root` in `style.css` | Every colour is a custom property, declared twice: the dark value alone, then the `light-dark()` pair. A browser without `light-dark()` keeps the dark value and the site still works. No colour anywhere else in the stylesheet. |
| Themes | `color-scheme: light dark`, toggle pins `only dark` / `only light` | The default follows the OS. The toggle is the word `theme: auto` cycling auto → light → dark, because two icons cannot express three states. |
| Accents | `--cyan` `--pink` `--purple` `--yellow` | Not shared between themes: the dark set is tuned for black and every one of them fails 4.5:1 on paper, so the light values are separately measured equivalents. |
| Type | JetBrains Mono 300/400/700, monospace fallback stack | One face. The whole identity rests on it — a proportional face here would be a different product. |
| Measure | `--measure: 68ch` inside a `62rem` wrap | The margin this leaves on a wide screen is where the depth gauge lives; nothing else goes there. |
| Radius | `3px`, plus `50%` for actual circles | One corner language. |
| Motion | `--dur-fast: 140ms`, `--dur-slow: 320ms`, `--ease: cubic-bezier(0.25, 0, 0.2, 1)` | Two durations and one curve for every transition. Fast answers the pointer; slow follows the theme or moves something. |
| Terminal blocks | `.term` carries a fixed palette and `color-scheme: only dark` | A picture of a terminal does not invert when the page does. `@media print` is the one exception — on paper it is text, not a screen. |
| Sea | `--sea-0`..`--sea-3`, `--sea-deep`, `--sea-abyss`, `--sea-foam`, `--whale*`, `--sky-star*` | `waves.js` paints by resolving these off `:root`, so both themes stay in the stylesheet and the toggle just works. Colour literals in that file are a defect. |

## The two gestures

The tool is named for two verbs and the page performs both. They are the
only motion on the site that is not a hover state, and both answer
something the reader did — nothing here moves at a reader sitting still
except the water itself, which is scenery.

**Deep — scrolling descends.** One scalar, `scroll.depth`, is scroll
position over the scrollable range. `waves.js` owns it, publishes it to
CSS as `--depth` on `:root`, and every part of the descent reads that one
number: the surface climbs out of the frame with parallax, the stars go
out over the first eighth, marine snow streams upward, the water darkens
toward `--sea-abyss`, and the whale sinks more slowly than the surface so
it is still there — dim — when you are in the dark. The gauge in the right
margin is the same number as text.

**Seek — clicking pings.** A click sends two things: a wave packet into
the water (what the water does) and a sonar ring from the pointer (what
you did). When the ring reaches the whale, the whale's outline answers and
fades. Down deep that echo is the only time you see it clearly, which is
the argument for the whole mechanism. Clicks on controls get the same
pulse from the control's own edge, in CSS, so the page has one click
language rather than one for scenery and one for buttons.

Both stop completely under `prefers-reduced-motion`: the sea paints one
still frame, no ping is emitted at all (a ring that cannot travel is a
circle left on the page), and no control pulses.

## Responsive

Four breakpoints, each answering a different question — which is why they
are not one number:

- **≥1100px** — the depth gauge appears in the right margin. It is the
  only thing that ever goes there.
- **≤52rem** — the masthead stacks: brand row, then one horizontally
  scrolling nav strip that bleeds to both edges with a mask fade as its
  affordance. This is arithmetic, not comfort — brand, nav and toggle need
  about 760px of row between them. The theme toggle lifts out of the strip
  onto the brand row, because a preference control you have to scroll to
  find is the one control on the site you cannot see. The masthead is
  sticky, so a row saved here is a row saved on every screenful.
- **≤36rem** — tables get the same edge fade, because a table narrower
  than its `34rem` minimum hides the column carrying the actual
  explanation and puts the scrollbar thirteen rows below the fold.
- **≤640px** — type drops to 14px. Reading comfort, nothing structural.
- No page ever scrolls horizontally. `.term` blocks and tables scroll
  inside themselves; the document does not. This is checkable and worth
  checking after any layout change.

## Hard bans

`site/bans.sh`, wired into `make site-check`. Each one encodes a scar, not
a preference; the why is in `TASTE.md` or beside the code:

1. A hardcoded colour outside the token block.
2. A colour literal in `waves.js` outside a `var()` fallback.
3. A corner radius outside `3px` / `50%` / `inherit`.
4. A second font family.
5. An urgency animation — pulse, blink, bounce, flash, shake.
6. A missing `prefers-reduced-motion` block.

## Behavioral floor

Never traded away for a surface: text readable with JavaScript off, every
colour pair above 4.5:1 against its background in both themes, controls
that name their state rather than implying it with an icon, and a document
that does not move sideways. When a change is proposed, judge behaviour
before surfaces and count what it changes — a redesign that improves the
screenshot and costs a reader the third column of a table has lost.
