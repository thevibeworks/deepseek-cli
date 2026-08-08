#!/usr/bin/env bash
# Design bans for the site — deterministic checks that encode scars.
#
# These are not style preferences. Each one is a rule that broke something
# real, written down so it cannot break it again; the why for each lives in
# TASTE.md and DESIGN.md. A clean run is evidence, not proof — it cannot
# see costume, so pair it with a look at the actual page.
#
#   site/bans.sh          # check
#
# Exit 1 if any ban trips.

set -uo pipefail

cd "$(dirname "$0")" || exit 1
fail=0

hit() { echo "DEFECT [$1]: $2"; fail=1; }

# 1. Colour lives in the token layer.
#
#    Every colour on the site is a custom property on :root, declared once
#    for dark and once as a light-dark() pair. That is what lets waves.js
#    paint the sea by reading CSS, and what makes the theme toggle one
#    property rather than a second stylesheet. A hex further down the file
#    is a colour the other theme cannot reach.
#
#    Three exemptions, all deliberate and all documented where they sit:
#    the .term and .pg-command palettes (a picture of a terminal does not
#    invert when the page does), the @media print block (paper is paper),
#    and mask-image stops (#000 there means "opaque", not a colour).
#
#    Interval expressions and \b are not portable across awk flavours, so
#    the hex match is spelled out longhand — a ban that silently matches
#    nothing is worse than no ban, and this one did exactly that until it
#    was tested against a deliberate violation.
out=$(awk '
  /^:root/ { intoken = 1 }
  /^}/     { if (intoken) intoken = 0 }
  /--term-/ || /@media print/ || /mask-image/ { next }
  /^ *background: #fff/ || /^ *color: #000/   { next }
  /^ *\*/  { next }
  !intoken && /#[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]/ { print FILENAME ":" NR ": " $0 }
' style.css)
if [ -n "$out" ]; then
  hit token "hardcoded colour outside the token block"
  echo "$out" | head -20
fi

# 2. The sea's colours are CSS's business, not JavaScript's.
#
#    waves.js resolves every colour it paints with through a CSS custom
#    property, which is the only reason the two themes are settled in one
#    place. Colour literals in there are allowed in exactly one shape: the
#    var() fallbacks inside readPalette, which exist so a stylesheet
#    missing a token degrades to something rather than to `undefined`.
if out=$(grep -nE "#[0-9a-fA-F]{3,8}\b|rgba?\([0-9]" waves.js |
  grep -vE "read\(|^\s*[0-9]+:\s*//|fallback"); then
  hit js-colour "colour literal in waves.js outside a token fallback"
  echo "$out" | head -20
fi

# 3. One corner language.
#
#    3px everywhere, 50% for the things that are actually circles, and
#    `inherit` for a pseudo-element tracing its own parent. A fourth value
#    is a second design speaking over the first.
if out=$(grep -nE 'border-radius:' style.css | grep -vE ':\s*(3px|50%|inherit|0)\s*;'); then
  hit radius "a corner radius outside the one language (3px / 50% / inherit)"
  echo "$out" | head -20
fi

# 4. One face.
#
#    The site is set in JetBrains Mono with a monospace fallback stack, and
#    that single choice is doing a lot of the identity work. A second family
#    is a decision that should be argued for in TASTE.md first.
if out=$(grep -nE "font-family:" style.css | grep -vE "JetBrains Mono|inherit"); then
  hit font "a second font family"
  echo "$out" | head -20
fi

# 5. No urgency devices.
#
#    Pulsing, blinking and bouncing spend the reader's trust to buy their
#    attention. This site's motion answers something the reader did — the
#    pointer, a click, the scroll position — and there is no animation on
#    it that runs at a reader who is sitting still. The sea is the one
#    exception and it earns it by being scenery, off-screen-aware, and
#    stopped entirely under prefers-reduced-motion.
if out=$(grep -nE "animation:.*(pulse|blink|bounce|flash|shake)" style.css); then
  hit urgency "an urgency animation"
  echo "$out" | head -20
fi

# 6. Motion asks permission.
#
#    Anything that moves has to have an answer for a reader who asked the
#    OS not to. The global reduce block is that answer for transitions and
#    animations; this checks it is still there, because deleting it is a
#    one-line accident with a whole-site blast radius.
if ! grep -q "prefers-reduced-motion" style.css; then
  hit reduced-motion "style.css has no prefers-reduced-motion block"
fi

if [ "$fail" -eq 0 ]; then
  echo "bans: clean"
fi
exit "$fail"
