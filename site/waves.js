// Live water, a whale in it, at night a sky of stars over it — and a page
// that sinks through the lot of it as you read.
//
// The name of the tool is two verbs, and the page performs both. Scrolling
// is *deep*: the surface climbs out of the frame, the stars go out, marine
// snow starts streaming up past you, and the water darkens toward the
// abyss. Clicking is *seek*: a sonar ring goes out from the pointer, and
// when it reaches the whale the whale answers. Down in the dark that
// return is the only way to see it, which is the whole idea — the deeper
// you are, the more you have to ping for what is down there with you.
//
// Both are driven by one scalar each — `scroll.depth` for the descent,
// the ping's own age for the seek — so nothing here is on a timer that
// runs whether you are looking or not.
//
// Four wave layers, each a sum of three sines, drawn back to front onto a
// 2D canvas. The whale is filled between layer 1 and layer 2, so the two
// nearest layers wash over it — that draw order, and nothing else, is what
// makes it read as *in* the water rather than pasted on top of it.
//
// The stars exist only on the dark theme, and that is decided in CSS, not
// here: their colour tokens resolve to fully transparent by day, and a
// star with no colour is not drawn. The field itself is deterministic —
// seeded from each star's index — so the same sky comes back on every
// visit, and the DOM test can look at a reproducible frame. Sizes follow
// a power law (a few bright, many faint), the faint ones twinkle more
// than the bright ones and each on its own slow period, and everything
// dims toward the horizon the way a real sky does. That set of rules,
// rather than uniform white dots blinking in step, is the difference
// between a night sky and a string of fairy lights.
//
// The interaction is all in the surface, never in the whale: the water
// rises toward the pointer, a click sends a travelling packet out from
// where you clicked, and scrolling fast makes it choppy. The whale just
// rides whatever the surface is doing — its height and its tilt are read
// back out of the same height field, which is why it looks like it is
// floating instead of being animated on a timer.
//
// Scroll is read twice, and the two readings do different jobs. Scroll
// *speed* roughs the surface up (`scroll.energy`); scroll *position* is
// how deep you are (`scroll.depth`). Position is the one that carries the
// descent, and it is published to CSS as `--depth` so the page furniture
// — the gauge in the margin — descends on the same number rather than on
// a second scroll listener that could disagree with this one.
//
// No dependencies, no build step, no WebGL. It runs at a few hundred
// sine calls per frame, which is nothing, and it stops entirely when it
// is off-screen, when the tab is hidden, or when the reader has asked for
// reduced motion (in which case they get one still frame, which is a
// picture rather than an absence).
//
// Colours are not in here. Every one comes from a CSS custom property on
// :root, so the light and dark themes are settled in style.css where the
// rest of the palette lives, and the theme toggle just works.
(function (global) {
  'use strict';

  // ---------------------------------------------------------------- whale

  // A blue whale, facing left, in a 1000x430 box. Hand-authored and, this
  // time, actually looked at — drawn against a browser rather than blind:
  // the blunt rostrum with the splash-guard bump in front of the blowhole,
  // a long back peaking just forward of middle, the small falcate dorsal
  // fin three-quarters back, a tail stock that thins and rises, and the
  // fluke lifted the way a whale lifts it before sounding. The fluke is
  // drawn three-quarter the way every whale mark since Moby-Dick has
  // cheated it — a true side view would show it edge-on as a line, and
  // nobody reads that as a whale. Two lobes and the notch between them
  // are what make it a fluke instead of a shark fin, which is what the
  // previous single-lobe tail kept reading as.
  //
  // Landmarks, for the next editor: rostrum tip (30,208), splash guard
  // (229,176), back peak (471,157), dorsal tip (731,161), fluke root
  // (843,193), upper lobe tip (956,~97), notch (920,175), lower lobe tip
  // (966,~188), belly deepest (540,272), jaw corner (110,238).
  //
  // The flipper is a second path rather than part of the body because it
  // overlaps it. Filled together under the nonzero rule the overlap
  // cancels and punches a hole in the belly; filled separately it can
  // also take a darker shade, which reads as the far flipper and gives
  // the silhouette its only hint of depth. The mouth is a stroked line,
  // not part of the fill — a silhouette with a jaw drawn into its outline
  // stops being a silhouette; a faint line across it stays one, and the
  // mouth line is most of what makes the head read as a whale's.
  var WHALE = {
    view: { w: 1000, h: 430 },
    body: 'M30 208C58 202 92 198 122 195C152 192 186 187 206 183'
        + 'C216 181 223 177 229 176C237 175 245 179 251 181'
        + 'C276 176 306 171 331 168C371 162 431 157 471 157'
        + 'C516 158 561 162 601 166C641 170 676 174 703 177'
        + 'C713 172 723 164 731 161C735 160 735 164 734 167'
        + 'C737 171 741 173 745 174C763 179 783 186 799 191'
        + 'C815 195 831 196 843 193C860 184 878 170 894 154'
        + 'C914 134 934 110 945 98C952 91 959 94 956 103'
        + 'C951 124 942 148 934 162C930 167 924 172 920 175'
        + 'C937 175 954 179 966 184C973 187 972 192 964 193'
        + 'C943 196 918 202 900 210C881 218 862 222 850 224'
        + 'C805 238 748 248 700 254C650 260 590 269 540 272'
        + 'C480 275 415 272 360 268C305 264 245 258 200 252'
        + 'C168 248 133 243 110 238C86 234 60 232 48 229'
        + 'C38 226 26 216 30 208Z',
    fin: 'M258 244C284 258 320 282 352 298C374 308 390 313 385 317'
       + 'C378 322 340 310 310 293C286 279 264 260 252 248Z',
    mouth: 'M34 218C70 226 106 233 138 240',
    eye: { x: 152, y: 227, r: 6 },
    blow: { x: 230, y: 172 },
  };

  // The site's mark: DeepSeek's own whale, verbatim from the official
  // docs favicon (api-docs.deepseek.com/img/favicon.svg), in its native
  // 63.12x46.40 box. It replaced the home-drawn sounding fluke on
  // 2026-08-12 - the site now wears the upstream mark rather than a
  // cousin of it. The favicon and the masthead carry this path
  // verbatim; waves.test.js pins both, because nobody reviews a
  // favicon and a drifted mark would never be noticed by looking.
  var MARK = 'M62.4575 3.89441C61.7888 3.56726 61.501 4.1908 61.1101 4.50769C60.9763 4'
    + '.60999 60.863 4.7428 60.75 4.86548C59.7727 5.9082 58.6311 6.59302 57.139'
    + '4 6.51123C54.9587 6.38855 53.0969 7.07349 51.4512 8.73975C51.1013 6.6850'
    + '6 49.939 5.45837 48.1699 4.67126C47.2441 4.26233 46.3081 3.85352 45.6599'
    + ' 2.96411C45.2073 2.33032 45.084 1.625 44.8577 0.929932C44.7136 0.510864 '
    + '44.5696 0.081543 44.0862 0.0098877C43.5615 -0.0718994 43.3557 0.367676 4'
    + '3.1501 0.735718C42.3271 2.2384 42.0083 3.89441 42.0391 5.5708C42.1111 9.'
    + '34277 43.7056 12.3481 46.8738 14.4846C47.2336 14.73 47.3264 14.9753 47.2'
    + '131 15.333C46.9971 16.0691 46.74 16.7847 46.5137 17.5206C46.3696 17.9908'
    + ' 46.1538 18.093 45.6497 17.8887C43.9114 17.1628 42.4094 16.0895 41.0825 '
    + '14.7913C38.8298 12.6139 36.7932 10.2117 34.2524 8.33081C33.6558 7.89124 '
    + '33.0593 7.48242 32.4421 7.09399C29.8499 4.57922 32.7815 2.5144 33.4604 2'
    + '.26904C34.1702 2.01343 33.7073 1.1344 31.4133 1.14465C29.1196 1.15479 27'
    + '.0212 1.92151 24.3467 2.94373C23.9558 3.09705 23.5444 3.20947 23.1226 3.'
    + '30151C20.6951 2.84143 18.1748 2.73926 15.5415 3.03577C10.5835 3.58777 6.'
    + '62329 5.92859 3.7124 9.92554C0.215088 14.73 -0.60791 20.1886 0.400146 25'
    + '.8824C1.45972 31.8828 4.5249 36.8508 9.23608 40.7354C14.1221 44.7629 19.'
    + '7488 46.7357 26.1675 46.3575C30.0659 46.1327 34.4067 45.6113 39.303 41.4'
    + '713C40.5374 42.0847 41.8335 42.33 43.9834 42.514C45.6394 42.6674 47.2336'
    + ' 42.4323 48.468 42.1766C50.4019 41.7678 50.2683 39.9789 49.5688 39.6517C'
    + '43.9009 37.0144 45.1455 38.0878 44.0142 37.2189C46.8943 33.8148 51.2351 '
    + '30.278 52.9324 18.8188C53.0662 17.9091 52.9529 17.3367 52.9324 16.6006C5'
    + '2.9221 16.1509 53.0249 15.9771 53.5393 15.9259C54.9587 15.7625 56.3372 1'
    + '5.3739 57.6023 14.6788C61.2747 12.6753 62.7559 9.38367 63.1055 5.43799C6'
    + '3.157 4.83484 63.0952 4.2113 62.4575 3.89441ZM30.4568 39.4065C24.9639 35'
    + '.0927 22.2998 33.6718 21.199 33.7332C20.1704 33.7944 20.3557 34.97 20.58'
    + '18 35.7367C20.8186 36.493 21.1272 37.0144 21.5591 37.6788C21.8574 38.118'
    + '4 22.0632 38.7727 21.2607 39.2633C19.4915 40.3571 16.416 38.8953 16.272 '
    + '38.8237C12.6924 36.718 9.69897 33.9375 7.59033 30.1349C5.55347 26.4753 4'
    + '.37061 22.5499 4.17529 18.3589C4.12378 17.3468 4.42212 16.989 5.43018 16'
    + '.8051C6.75708 16.5597 8.12524 16.5087 9.45215 16.7029C15.0581 17.5206 19'
    + '.8311 20.025 23.8323 23.9913C26.116 26.2504 27.844 28.9491 29.6235 31.58'
    + '64C31.5164 34.3873 33.553 37.0553 36.145 39.2429C37.0605 40.0095 37.791 '
    + '40.5922 38.4905 41.0215C36.3816 41.2567 32.8638 41.3077 30.4568 39.4065Z'
    + 'M33.0901 22.4886C33.0901 22.0388 33.4502 21.681 33.9026 21.681C34.0056 2'
    + '1.681 34.0981 21.7015 34.1804 21.7322C34.2935 21.7731 34.3965 21.8344 34'
    + '.4788 21.9264C34.6228 22.0695 34.7051 22.2739 34.7051 22.4886C34.7051 22'
    + '.9384 34.345 23.2961 33.8923 23.2961C33.4397 23.2961 33.0901 22.9384 33.'
    + '0901 22.4886ZM41.2676 26.6798C40.7432 26.8944 40.2185 27.0784 39.7144 27'
    + '.0989C38.9326 27.1398 38.0789 26.8229 37.616 26.4344C36.896 25.8313 36.3'
    + '816 25.494 36.1658 24.441C36.073 23.9913 36.1245 23.2961 36.2068 22.8975'
    + 'C36.3921 22.0388 36.1863 21.4868 35.5793 20.986C35.0857 20.577 34.4583 2'
    + '0.4646 33.769 20.4646C33.5117 20.4646 33.2751 20.3522 33.1003 20.2601C32'
    + '.8123 20.1171 32.5757 19.7593 32.802 19.3197C32.874 19.1766 33.2239 18.8'
    + '291 33.3062 18.7677C34.2422 18.2362 35.3223 18.4099 36.3201 18.8086C37.2'
    + '458 19.1869 37.9453 19.882 38.9534 20.8633C39.9819 22.0491 40.167 22.376'
    + '2 40.7534 23.2655C41.2163 23.9607 41.6379 24.6761 41.926 25.494C42.1008 '
    + '26.0051 41.8745 26.4242 41.2676 26.6798Z';

  // Where the resting waterline crosses the whale, as a fraction of the
  // box height. 0.42 is box y=181, just above the back at y=150, so only
  // the spine and the upper fluke lobe break the surface and everything
  // else is under it.
  //
  // Submerged is the whole point at this size. The whale is wider than
  // the viewport; drawn riding high it stops being scenery and becomes an
  // obstruction sitting on top of the page. Under the water the two near
  // layers tint and dim it along its whole length, so you read the mass
  // without it ever competing with a paragraph.
  var WATERLINE = 0.42;

  // ---------------------------------------------------------------- waves

  // Farthest first. `base` is the resting waterline as a fraction of the
  // canvas height, so every layer is one horizon further down the frame.
  // Nearer layers are faster, choppier and more opaque; `reach` is how
  // much of the pointer and click energy they pick up, which is the same
  // gradient — a swell on the horizon does not care where your mouse is.
  //
  // Speeds alternate sign so the layers slide across each other instead
  // of marching in step, which is most of what sells the parallax. They
  // are also slow on purpose: water this size heaves, it does not ripple,
  // and a surface that visibly hurries reads as a screensaver.
  var LAYERS = [
    { base: 0.58, amp: 0.030, crests: 1.5, speed: 0.19, reach: 0.28, alpha: 0.22, foam: 0.00, tint: 0 },
    { base: 0.68, amp: 0.026, crests: 2.3, speed: -0.27, reach: 0.52, alpha: 0.28, foam: 0.06, tint: 1 },
    { base: 0.78, amp: 0.022, crests: 3.2, speed: 0.36, reach: 0.78, alpha: 0.38, foam: 0.13, tint: 2 },
    { base: 0.89, amp: 0.018, crests: 4.5, speed: -0.48, reach: 1.00, alpha: 0.56, foam: 0.22, tint: 3 },
  ];

  // The whale is drawn immediately before this layer, so everything from
  // here down is in front of it and LAYERS[WHALE_LAYER] is the surface it
  // rides.
  var WHALE_LAYER = 2;

  // The 404's sea is not the fixed full-viewport one: it is a band under
  // the masthead, with the waterline hard up against the header rule so
  // the whale rides the rule like a horizon — back, dorsal and fluke
  // breaking it, everything else below. Same rules as LAYERS, different
  // geometry; the fractions are of the band's height, not the viewport's.
  var BAND_LAYERS = [
    { base: 0.08, amp: 0.050, crests: 2.0, speed: 0.16, reach: 0.28, alpha: 0.22, foam: 0.00, tint: 0 },
    { base: 0.14, amp: 0.044, crests: 2.9, speed: -0.22, reach: 0.52, alpha: 0.28, foam: 0.06, tint: 1 },
    { base: 0.21, amp: 0.038, crests: 4.0, speed: 0.30, reach: 0.78, alpha: 0.38, foam: 0.13, tint: 2 },
    { base: 0.42, amp: 0.032, crests: 5.5, speed: -0.40, reach: 1.00, alpha: 0.56, foam: 0.22, tint: 3 },
  ];

  // What differs between the two seas, in one place. `span` is the whale
  // as a fraction of the width: scenery you cannot see the ends of on a
  // page, an emblem you can see all of in the band. `sky` is the fraction
  // of the box above the far waterline, where the stars live — a strip
  // of nothing in the band, whose waterline kisses the header rule.
  var PROFILES = {
    viewport: { layers: LAYERS, sky: 0.52, span: 1.0, minSpan: 420, narrowBoost: 0.35 },
    band: { layers: BAND_LAYERS, sky: 0.05, span: 0.34, minSpan: 300, narrowBoost: 0.30 },
  };

  // ---------------------------------------------------------------- depth

  // How far the surface climbs out of the frame over a full descent, in
  // canvas heights. Every layer has to clear the top edge by the bottom of
  // the page or the "deep" reads as "the sea moved up a bit".
  var DIVE_RISE = 0.95;

  // ... and the parallax while it climbs. Near layers rise faster than far
  // ones, which is the same rule that makes the surface read as a surface
  // when it is sitting still. `reach` is already a nearness gradient, so
  // it does this job too rather than a second table of numbers.
  var DIVE_PARALLAX = 0.5;

  // The whale sinks with you, but not as fast — it lags the surface, so it
  // is still in frame (dim, and well above you) once you are in the dark
  // rather than having left with the light. Losing the animal entirely at
  // the point the page gets interesting is not a descent, it is an empty
  // canvas.
  var WHALE_LAG = 0.55;

  // The surface for one layer, at this depth. Everything that asks where
  // the water is goes through here, including the whale, so the descent
  // costs one term rather than a special case per caller.
  // A state with no depth in it is at the surface. Defaulting rather than
  // requiring it keeps `surfaceAt` callable with the state a caller would
  // naturally write, which is how it was callable before there was a
  // descent at all.
  function layerBase(layer, st) {
    var d = st.depth || 0;
    return layer.base - d * DIVE_RISE * (1 - DIVE_PARALLAX + layer.reach * DIVE_PARALLAX);
  }

  // The same state at a different depth, for a caller that wants to know
  // where the water would be if it had not sunk so far. Only the whale
  // uses it, and it borrows the live ripples and bulge by reference so the
  // surface it rides is the one everyone else can see.
  function shallow(st, depth) {
    // Copied wholesale rather than field by field: the profile fields
    // (layers, span floors) live on this state too, and a list of names
    // here would quietly drop whatever was added last.
    var out = {};
    for (var k in st) out[k] = st[k];
    out.depth = depth;
    return out;
  }

  // ---------------------------------------------------------------- seek

  // A ping is a sonar pulse: one expanding ring, thinning and fading as it
  // spends itself. It is drawn over the water rather than in it because it
  // is not water — it is the instrument, and instruments read on top.
  var PING_LIFE = 2.6;     // seconds until it is spent
  var PING_SPEED = 0.58;   // canvas widths per second

  // The return. When a ring's radius passes the whale, the whale answers
  // for about as long as it takes to notice — bright, then gone.
  var ECHO_BAND = 0.06;    // how near the ring has to be, as a fraction of width
  var ECHO_DECAY = 1.7;    // per second

  function pingRadius(p, t, w) { return (t - p.t0) * PING_SPEED * w; }

  // ---------------------------------------------------------------- stars

  // The sky band: stars live between the top of the frame and just above
  // the farthest layer's resting waterline, so none of them ever sits in
  // the water. Owned by the viewport profile; aliased here because the
  // field arithmetic predates the profiles.
  var SKY = PROFILES.viewport.sky;

  // Deterministic pseudo-random in [0, 1), seeded by star index and a
  // salt per property. Math.random would deal a different sky on every
  // visit and make the rendered frame untestable; this way star #17 is
  // the same star tomorrow.
  function starRand(i, salt) {
    var x = Math.sin(i * 127.1 + salt * 311.7) * 43758.5453;
    return x - Math.floor(x);
  }

  // Lay out the sky for a given viewport. Positions are fractions, so a
  // resize rescales the same constellations rather than dealing new ones.
  // `sky` is the fraction of the box that is sky; the band sea has a thin
  // strip of it, so the clamps come down with it or the strip snows.
  function starField(w, h, sky, lo, hi) {
    sky = sky == null ? SKY : sky;
    lo = lo == null ? 70 : lo;
    hi = hi == null ? 220 : hi;
    // Density by sky area, clamped: a phone still gets a sky worth
    // having, a cinema display does not get a snowstorm.
    var n = Math.max(lo, Math.min(hi, Math.round((w * h * sky) / 7200)));
    var stars = [];
    for (var i = 0; i < n; i++) {
      var m = starRand(i, 4);   // magnitude, 0 faint .. 1 bright
      stars.push({
        x: starRand(i, 1),
        y: starRand(i, 2) * sky,
        // Squaring the magnitude is the power law: most stars small,
        // a handful genuinely bright.
        r: 0.5 + m * m * 1.7,
        a: 0.22 + m * 0.6,
        warm: starRand(i, 3) < 0.24,
        // Faint stars twinkle hard, bright ones barely — small apertures
        // scintillate. Uniform twinkle is the fairy-light look.
        tw: 0.1 + (1 - m) * 0.42,
        spd: 0.6 + starRand(i, 5) * 1.7,
        phase: starRand(i, 6) * Math.PI * 2,
      });
    }
    return stars;
  }

  // Sun glitter: the day sea's answer to the night sky. Points scattered
  // over the water that catch the light for a moment each — the sparkle a
  // real sea throws when the sun is on it. Deterministic for the same
  // reason the stars are, and owned by the same CSS contract in reverse:
  // the sparkle token resolves to fully transparent on the dark theme,
  // where the stars take over.
  function glitterField(w, h) {
    var n = Math.max(24, Math.min(90, Math.round((w * h) / 26000)));
    var pts = [];
    for (var i = 0; i < n; i++) {
      pts.push({
        x: starRand(i, 11),
        // Which surface the glint rides, biased toward the near water
        // where the light would actually be.
        layer: starRand(i, 12) < 0.4 ? 2 : 3,
        len: 2.5 + starRand(i, 13) * 4.5,
        a: 0.3 + starRand(i, 14) * 0.5,
        spd: 0.5 + starRand(i, 15) * 1.4,
        phase: starRand(i, 16) * Math.PI * 2,
      });
    }
    return pts;
  }

  // ---------------------------------------------------------------- snow

  // Marine snow: the drift of dead matter falling through the water
  // column, and the one cue that says *descending* rather than merely
  // *dark*. It streams up past you because you are going down — the
  // movement is yours, not its, which is why the parallax below is keyed
  // to depth and not to the clock. The slow constant sink on top of that
  // is the snow's own, and it is what keeps the field alive when you stop
  // scrolling.
  var SNOW_START = 0.10;   // depth at which any of it is visible
  var SNOW_FULL = 0.34;    // ... and at which it is at full strength
  var SNOW_RISE = 2.4;     // screens travelled per unit of depth, nearest fleck

  // Dealt from the same seeded generator as the sky, on its own salts: a
  // reproducible field, for the same two reasons the stars are one.
  function snowField(w, h) {
    var n = Math.max(40, Math.min(150, Math.round((w * h) / 12000)));
    var out = [];
    for (var i = 0; i < n; i++) {
      var near = starRand(i, 21);            // 0 far .. 1 near
      out.push({
        x: starRand(i, 22),
        y: starRand(i, 23),
        // Nearer flecks are bigger, brighter and move more. One parameter
        // driving all three is what makes a flat field read as a volume.
        r: 0.6 + near * near * 2.2,
        a: 0.14 + near * 0.32,
        near: 0.25 + near * 0.75,
        // A little sideways set, so it is a drift rather than a lift.
        drift: (starRand(i, 24) - 0.5) * 0.05,
        sink: 0.010 + starRand(i, 25) * 0.026,
      });
    }
    return out;
  }

  // Is a resolved colour worth drawing at all? The star tokens resolve to
  // fully-transparent on the light theme, which is how CSS says "by day
  // there are no stars" without this file knowing what a theme is.
  function visibleColour(c) {
    if (!c) return false;
    c = String(c).trim();
    if (c === 'transparent') return false;
    return !/^rgba\((?:[^,]+,){3}\s*0(?:\.0+)?\s*\)$/.test(c) &&
      !/\/\s*0(?:\.0+)?\s*\)$/.test(c);
  }

  var RIPPLE_LIFE = 3.8;    // seconds until a click is fully forgotten
  var RIPPLE_SPEED = 0.40;  // canvas widths per second
  var RIPPLE_WIDTH = 0.09;  // packet envelope, as a fraction of width
  var BULGE_WIDTH = 0.13;   // pointer hill, as a fraction of width
  var BULGE_HEIGHT = 0.016; // ... and its peak, as a fraction of height

  // Sum of three sines. The multipliers are deliberately not integers:
  // at 1 : 2.17 : 3.71 the sum has no period short enough to see inside
  // one screen width, so the surface never visibly repeats itself.
  function waveAt(layer, x, t, w) {
    var k = (2 * Math.PI * layer.crests) / w;
    var s = t * layer.speed;
    return layer.amp * (
      1.00 * Math.sin(k * x + s) +
      0.42 * Math.sin(k * 2.17 * x - s * 1.31 + 1.7) +
      0.23 * Math.sin(k * 3.71 * x + s * 0.67 + 3.9)
    );
  }

  // A click becomes an outward-travelling wave packet: a carrier under a
  // Gaussian envelope whose centre moves away from the origin at a fixed
  // speed. Squaring the fade makes it die out of sight rather than
  // stopping, which is the difference between water and a light switch.
  function rippleAt(r, x, t, w) {
    var age = t - r.t0;
    if (age < 0 || age > RIPPLE_LIFE) return 0;
    var sigma = RIPPLE_WIDTH * w;
    var travel = Math.abs(x - r.x) - RIPPLE_SPEED * w * age;
    var env = Math.exp(-(travel * travel) / (2 * sigma * sigma));
    var fade = 1 - age / RIPPLE_LIFE;
    // One long swell under the envelope, not a zigzag: at 1.9 sigma the
    // packet holds about one visible ring, which is what a splash looks
    // like from shore. The old 1.15 put three crests inside it and read
    // as a wobble, not a wave.
    return r.amp * fade * fade * env * Math.cos((travel * 2 * Math.PI) / (sigma * 1.9));
  }

  // The water is pulled up toward the pointer — negative, because y grows
  // downward. `strength` is eased by the caller so it swells and settles
  // instead of snapping on the first mousemove.
  function bulgeAt(b, x, w, h) {
    if (b.strength <= 0.0015) return 0;
    var sigma = BULGE_WIDTH * w;
    var d = x - b.x;
    return -b.strength * BULGE_HEIGHT * h * Math.exp(-(d * d) / (2 * sigma * sigma));
  }

  // The whole height field for one layer at one x. Everything else reads
  // the surface through this, including the whale. Scroll chop roughens
  // the water; past about double the resting amplitude it stops reading
  // as swell and starts reading as static.
  function surfaceAt(layer, x, t, w, h, st) {
    var y = layerBase(layer, st) * h + waveAt(layer, x, t, w) * h * (1 + st.chop * 0.7);
    var e = bulgeAt(st.bulge, x, w, h);
    for (var i = 0; i < st.ripples.length; i++) e += rippleAt(st.ripples[i], x, t, w);
    return y + e * layer.reach;
  }

  // ------------------------------------------------------------ transform

  // Whale-box coordinates to screen. Exported because the spout comes out
  // of the blowhole, which is a point on the whale, and getting this
  // wrong is otherwise only visible as water appearing from its ear.
  function whaleToScreen(px, py, tr) {
    var lx = (px - WHALE.view.w / 2) * tr.sx;
    var ly = (py - WHALE.view.h * WATERLINE) * tr.sy;
    var c = Math.cos(tr.rot);
    var s = Math.sin(tr.rot);
    return { x: tr.x + lx * c - ly * s, y: tr.y + lx * s + ly * c };
  }

  // Below this width the whale is widened and re-centred. The fraction
  // that reads as "huge" on a desktop reads as "a small whale, some way
  // off" on a phone, because the band around it does not shrink at the
  // same rate; and once it is nearly as wide as the screen it has to be
  // centred or the fluke goes over the edge.
  var NARROW = 700;

  var SLOPE_SPAN = 0.42;  // where along itself the whale feels the water
  var TILT_GAIN = 0.9;    // ... and how much of that slope it takes on
  var TILT_MAX = 0.055;   // ~3 degrees: a roll you feel, not a see-saw

  // Where the whale sits this frame, and how far over it is leaning. Both
  // come out of the height field rather than a clock: the tilt is the
  // slope of the surface it is floating on, sampled either side of it,
  // and its height is the surface height — a whale animated on its own
  // timer is a whale bobbing like a toy. Mass is the other half of the
  // illusion: the gain stays well under the slope and the ceiling is a
  // few degrees, so the animal follows the water the way something that
  // heavy would — late, and not very far.
  function whaleTransform(t, w, h, st) {
    var narrow = w < NARROW;
    // It reads the water through a shallower copy of the descent, which is
    // the whole of WHALE_LAG: the surface leaves, the whale stays a while.
    if (st.depth) st = shallow(st, st.depth * WHALE_LAG);
    // The profile decides the floor and the narrow-screen boost: the
    // viewport whale is deliberately wider than the viewport (an enormous
    // animal is one you cannot see the ends of), while the band whale is
    // an emblem and has to stay whole.
    var boost = st.narrowBoost != null ? st.narrowBoost : 0.35;
    var floor = st.minSpan != null ? st.minSpan : 420;
    var frac = narrow ? st.whaleSpan + boost : st.whaleSpan;
    var span = Math.max(w * frac, floor);
    var sx = span / WHALE.view.w;
    var x = w * st.whaleX + Math.sin(t * 0.07) * w * 0.01;
    // The instance's own layer table, so the band's whale rides the
    // band's water; direct callers get the viewport's.
    var lay = (st.layers || LAYERS)[WHALE_LAYER];
    // Sample the slope over the whale's own length, not some fixed small
    // distance. It is three wavelengths long; a body that size does not
    // follow the water under its middle, it bridges several crests and
    // averages them. Measuring locally instead had it pitching through
    // twenty-five degrees on a phone, which is a shipwreck, not a swell.
    var d = span * SLOPE_SPAN;
    var y = surfaceAt(lay, x, t, w, h, st);
    var slope = (surfaceAt(lay, x + d, t, w, h, st) - surfaceAt(lay, x - d, t, w, h, st)) / (2 * d);
    var rot = Math.atan(slope) * TILT_GAIN;
    return {
      x: x,
      y: y,
      sx: sx * st.facing,
      sy: sx,
      rot: Math.max(-TILT_MAX, Math.min(TILT_MAX, rot)),
      span: span,
    };
  }

  // --------------------------------------------------------------- colour

  var PALETTE_KEYS = ['--sea-0', '--sea-1', '--sea-2', '--sea-3'];

  // Read the palette out of CSS rather than hard-coding it here, so the
  // two themes stay in style.css with the rest of the tokens. `read` is a
  // parameter so this is testable without a document.
  //
  // `read` must hand back a colour canvas can actually parse. Reading the
  // custom property directly does not: getPropertyValue('--sea-0') returns
  // the *specified* value, and every token on this site is declared as
  // light-dark(). Custom properties are substituted lazily, so nothing
  // resolves that until it is used in a real property — canvas gets the
  // literal string "light-dark(#bfe4ef, #0a4d63)", addColorStop throws
  // SyntaxError, and every frame dies silently. That shipped once. See
  // cssResolver, which is the only correct way to do this.
  function readPalette(read) {
    var sea = PALETTE_KEYS.map(function (k) { return read(k, '#00c2e9'); });
    return {
      sea: sea,
      deep: read('--sea-deep', '#00131c'),
      // Where the light has stopped reaching. Distinct from --sea-deep,
      // which is the floor of a layer's gradient at the surface: this one
      // is the colour of being *in* it.
      abyss: read('--sea-abyss', '#00131c'),
      foam: read('--sea-foam', '#ffffff'),
      whale: read('--whale', '#0b2f4a'),
      whaleLit: read('--whale-lit', '#00c2e9'),
      glow: read('--sea-glow', 'rgba(0,194,233,0.12)'),
      // The far end of the glow has to be the same hue at zero alpha, not
      // the keyword `transparent` — that is transparent *black*, and a
      // gradient running to it puts a grey halo round the whale on the
      // light theme.
      glowFade: read('--sea-glow-fade', 'rgba(0,194,233,0)'),
      // Transparent fallbacks: a stylesheet without the sky tokens gets
      // no stars, not white ones on a cream page. Same rule for the
      // glitter, which is the stars' daytime counterpart.
      star: read('--sky-star', 'rgba(0,0,0,0)'),
      starWarm: read('--sky-star-warm', 'rgba(0,0,0,0)'),
      sparkle: read('--sea-sparkle', 'rgba(0,0,0,0)'),
    };
  }

  // Resolve a custom property to something canvas understands, by putting
  // it through a real property on a real element and reading back what the
  // engine computed. `color` is the one to use: it accepts every colour
  // syntax and computes to rgb()/rgba().
  //
  // The var() fallback does double duty — an undefined token resolves to
  // the default here rather than making the declaration invalid and
  // leaving `color` at whatever it inherited.
  // The same colour at a chosen alpha. Everything cssResolver hands back
  // has been through `getComputedStyle().color`, so it is `rgb(r, g, b)`
  // or `rgba(r, g, b, a)` and nothing else — which is what makes this
  // three lines instead of a colour parser. A string in any other shape is
  // handed back untouched rather than turned into a canvas exception.
  function withAlpha(colour, a) {
    var m = /^rgba?\(([^)]+)\)$/.exec(String(colour).trim());
    if (!m) return colour;
    var p = m[1].split(',');
    if (p.length < 3) return colour;
    var base = p.length > 3 ? parseFloat(p[3]) : 1;
    return 'rgba(' + p[0].trim() + ',' + p[1].trim() + ',' + p[2].trim() + ',' + (base * a) + ')';
  }

  function cssResolver(doc) {
    var probe = doc.createElement('span');
    probe.style.cssText = 'position:absolute;width:0;height:0;visibility:hidden';
    probe.setAttribute('aria-hidden', 'true');
    (doc.body || doc.documentElement).appendChild(probe);
    var view = doc.defaultView;
    return function (key, fallback) {
      probe.style.color = '';
      probe.style.color = 'var(' + key + ', ' + fallback + ')';
      var got = view.getComputedStyle(probe).color;
      return got || fallback;
    };
  }

  // ------------------------------------------------------------ instances

  // One scroll listener for the whole page, reporting two different
  // things. Every ocean reads the same `energy`, so scrolling roughs up
  // all of them together — which is what one body of water would do — and
  // the same `depth`, so they are all at the same depth, which is what one
  // body of water would also do.
  var scroll = { last: 0, energy: 0, depth: 0, wired: false };

  // The deepest the gauge will admit to. A round number rather than a
  // per-page one: a fixed scale means "-400 m" means the same thing on
  // every page, and the alternative — metres derived from document height
  // — would have a short page reaching the abyss in two flicks.
  var FLOOR_M = 1000;

  // Scroll position as a fraction of the scrollable range. A page shorter
  // than the window has no range and is therefore never deep — clamping
  // rather than dividing by zero is the whole of that case.
  function depthOf(win, doc) {
    var el = doc.documentElement || {};
    var body = doc.body || {};
    var height = Math.max(el.scrollHeight || 0, body.scrollHeight || 0);
    var range = height - (win.innerHeight || el.clientHeight || 0);
    if (!(range > 0)) return 0;
    return Math.max(0, Math.min(1, (win.pageYOffset || 0) / range));
  }

  // Publish the descent to the page: as `--depth` on the root, so CSS can
  // read it, and as text in any depth gauge. One number, one owner — a
  // second scroll listener somewhere in a stylesheet's worth of JS is how
  // the margin ends up disagreeing with the water.
  function publishDepth(doc, d) {
    var el = doc.documentElement;
    if (el && el.style && el.style.setProperty) {
      el.style.setProperty('--depth', d.toFixed(4));
    }
    // At the surface it is zero, not minus zero. The sign means "below",
    // and there is no below at the top of the page.
    var m = Math.round(d * FLOOR_M);
    var label = (m > 0 ? '-' + m : '0') + ' m';
    var gauges = doc.querySelectorAll ? doc.querySelectorAll('[data-depth-gauge]') : [];
    for (var i = 0; i < gauges.length; i++) {
      var read = gauges[i].querySelector && gauges[i].querySelector('.gauge-read');
      if (read) read.textContent = label;
    }
  }

  function wireScroll(win, doc) {
    if (scroll.wired || !win.addEventListener) return;
    scroll.wired = true;
    scroll.last = win.pageYOffset || 0;
    doc = doc || win.document;
    var sync = function () {
      var now = win.pageYOffset || 0;
      var dv = Math.abs(now - scroll.last);
      scroll.last = now;
      // Capped well under 1: a flick should stir the water, not turn it
      // to static. At the old ceiling a fast scroll doubled the swell
      // amplitude under the text you were trying to read.
      scroll.energy = Math.min(0.45, scroll.energy + dv / 600);
      scroll.depth = depthOf(win, doc);
      if (doc) publishDepth(doc, scroll.depth);
    };
    win.addEventListener('scroll', sync, { passive: true });
    // A resize changes the scrollable range, so the same offset is now a
    // different depth. Reloading onto an anchor lands mid-page too, which
    // is why this runs once here rather than only on the first scroll.
    win.addEventListener('resize', sync, { passive: true });
    sync();
  }

  function Ocean(host, opts) {
    opts = opts || {};
    var doc = host.ownerDocument;
    var win = doc.defaultView || global;

    var canvas = doc.createElement('canvas');
    canvas.className = 'sea-canvas';
    // Decorative: the page reads identically with it removed, and a
    // screen reader announcing "canvas" here would be pure noise.
    canvas.setAttribute('aria-hidden', 'true');
    host.appendChild(canvas);

    var ctx = canvas.getContext && canvas.getContext('2d');
    if (!ctx) return null;

    this.host = host;
    this.canvas = canvas;
    this.ctx = ctx;
    this.win = win;
    this.doc = doc;
    this.w = 0;
    this.h = 0;
    this.raf = 0;
    this.t = 0;
    this.last = 0;
    this.visible = true;
    this.frames = 0;
    this.pts = null;

    // `data-ocean="band"` is the 404's strip under the masthead; anything
    // else is the fixed full-viewport sea. The profile owns the geometry
    // that differs between them.
    var profile = PROFILES[opts.layout] || PROFILES.viewport;
    this.layout = profile === PROFILES.band ? 'band' : 'viewport';
    this.layers = profile.layers;
    this.sky = profile.sky;

    this.st = {
      chop: 0,
      depth: 0,
      echo: 0,
      ripples: [],
      pings: [],
      spout: [],
      bulge: { x: 0, strength: 0, target: 0 },
      whaleX: opts.whaleX != null ? opts.whaleX : 0.5,
      whaleSpan: opts.whaleSpan != null ? opts.whaleSpan : profile.span,
      facing: opts.facing === 'right' ? -1 : 1,
      nextSpout: 6,
      layers: profile.layers,
      minSpan: profile.minSpan,
      narrowBoost: profile.narrowBoost,
    };
    this.whale = opts.whale !== false;

    this.body = global.Path2D ? new global.Path2D(WHALE.body) : null;
    this.flipper = global.Path2D ? new global.Path2D(WHALE.fin) : null;
    this.mouth = global.Path2D ? new global.Path2D(WHALE.mouth) : null;

    this.palette = readPalette(function (_, fallback) { return fallback; });
    this.refreshPalette();
    this.resize();
    this.wire();
    // Always paint one frame before deciding whether to animate. Hidden
    // tabs, off-screen bands and reduced-motion all stop the *loop*;
    // none of them is a reason to show nothing at all. Mounting marks the
    // host live, which takes the CSS fallback away, so a mount that
    // painted nothing would leave a blank hole where the sea should be —
    // which is exactly what a page opened in a background tab used to get.
    this.draw(0);
    this.start();
  }

  Ocean.prototype.refreshPalette = function () {
    if (!this.win.getComputedStyle) return;
    if (!this.resolve) this.resolve = cssResolver(this.doc);
    this.palette = readPalette(this.resolve);
    // A sea that is not animating — reduced motion, hidden tab — is a
    // still picture, and a picture painted in the old theme's colours is
    // wrong the moment the theme changes. Repaint the one frame.
    if (!this.running()) this.draw(this.t);
  };

  Ocean.prototype.reduced = function () {
    var mm = this.win.matchMedia;
    return !!(mm && mm.call(this.win, '(prefers-reduced-motion: reduce)').matches);
  };

  Ocean.prototype.resize = function () {
    var rect = this.host.getBoundingClientRect
      ? this.host.getBoundingClientRect()
      : { width: 0, height: 0 };
    var w = Math.max(1, Math.round(rect.width || this.host.clientWidth || 0));
    var h = Math.max(1, Math.round(rect.height || this.host.clientHeight || 0));
    if (w === this.w && h === this.h) return false;
    this.w = w;
    this.h = h;
    // Cap the device pixel ratio at 2. A phone claiming 3 or 4 is asking
    // for nine to sixteen times the fill rate to render a gradient nobody
    // can see the pixels of.
    var dpr = Math.min(2, this.win.devicePixelRatio || 1);
    this.canvas.width = Math.round(w * dpr);
    this.canvas.height = Math.round(h * dpr);
    this.canvas.style.width = w + 'px';
    this.canvas.style.height = h + 'px';
    if (this.ctx.setTransform) this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    // ~220 samples across, so the step is a few pixels at any width.
    this.step = Math.max(4, w / 220);
    this.n = Math.ceil(w / this.step) + 2;
    this.pts = new Float64Array(this.n);
    // The star, glitter and snow counts all follow the frame's area, so a
    // resize deals every field again from the same seeds.
    this.stars = null;
    this.glints = null;
    this.snow = null;
    return true;
  };

  Ocean.prototype.wire = function () {
    var self = this;
    var win = this.win;
    wireScroll(win, this.doc);
    this.st.depth = scroll.depth;

    // The canvas is pointer-events: none so it never shadows a link, so
    // the pointer has to be tracked on the window and mapped in. The
    // water answers the pointer while it is over the water or just below
    // it — before this reached four hundred pixels past the sea, moving
    // the mouse anywhere on the page heaved the swell, and water that
    // answers everything reads as noise, not life.
    this.onMove = function (e) {
      var r = self.canvas.getBoundingClientRect();
      self.st.bulge.x = e.clientX - r.left;
      var near = e.clientY > r.top + r.height * 0.3 &&
                 e.clientY < r.bottom + 120;
      self.st.bulge.target = near ? 1 : 0;
    };
    this.onLeave = function () { self.st.bulge.target = 0; };
    this.onDown = function (e) {
      var r = self.canvas.getBoundingClientRect();
      self.splash(e.clientX - r.left, 1);
      self.ping(e.clientX - r.left, e.clientY - r.top);
    };

    if (win.addEventListener) {
      win.addEventListener('pointermove', this.onMove, { passive: true });
      win.addEventListener('pointerdown', this.onDown, { passive: true });
      win.addEventListener('blur', this.onLeave);
    }
    if (this.doc.addEventListener) {
      this.onVis = function () {
        if (self.doc.hidden) self.stop();
        else self.start();
      };
      this.doc.addEventListener('visibilitychange', this.onVis);
    }

    // The theme toggle writes data-theme onto <html>; the OS can change
    // it out from under us too. Either way the palette is now stale.
    if (win.MutationObserver) {
      this.mo = new win.MutationObserver(function () { self.refreshPalette(); });
      this.mo.observe(this.doc.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    }
    if (win.matchMedia) {
      var mq = win.matchMedia('(prefers-color-scheme: dark)');
      if (mq.addEventListener) {
        mq.addEventListener('change', function () { self.refreshPalette(); });
      }
      var rm = win.matchMedia('(prefers-reduced-motion: reduce)');
      if (rm.addEventListener) {
        rm.addEventListener('change', function () { self.stop(); self.start(); });
      }
    }

    if (win.ResizeObserver) {
      this.ro = new win.ResizeObserver(function () {
        if (self.resize() && !self.running()) self.draw(self.t);
      });
      this.ro.observe(this.host);
    } else if (win.addEventListener) {
      this.onResize = function () {
        if (self.resize() && !self.running()) self.draw(self.t);
      };
      win.addEventListener('resize', this.onResize);
    }

    // Off-screen oceans cost nothing. On a long docs page the footer band
    // is out of view almost all the time.
    if (win.IntersectionObserver) {
      this.io = new win.IntersectionObserver(function (entries) {
        self.visible = entries[entries.length - 1].isIntersecting;
        if (self.visible) self.start();
        else self.stop();
      }, { rootMargin: '120px' });
      this.io.observe(this.host);
    }
  };

  Ocean.prototype.splash = function (x, amp) {
    // Clicks are picked up from the window, so a click at the top of a
    // long page reaches the band at the bottom of it too. Nobody can see
    // that one.
    if (!this.visible) return;
    // Cheap guard against a held-down mouse queueing hundreds of packets.
    if (this.st.ripples.length > 12) this.st.ripples.shift();
    this.st.ripples.push({ x: x, t0: this.t, amp: (amp || 1) * this.h * 0.042 });
    if (!this.running()) this.draw(this.t);
    // Clicking near the whale startles it into blowing.
    if (this.whale && Math.abs(x - this.w * this.st.whaleX) < this.w * 0.3) {
      this.st.nextSpout = Math.min(this.st.nextSpout, this.t + 0.25);
    }
  };

  // The seek half of the name: a sonar pulse from where you clicked.
  // Separate from `splash` because they are two different claims about the
  // same click — the splash is what the water does, the ping is what you
  // did. They also live for different lengths of time and are drawn on
  // opposite sides of the whale.
  Ocean.prototype.ping = function (x, y) {
    if (!this.visible) return;
    // A pulse that cannot travel is not a quieter pulse, it is a ring
    // painted on the page and left there — the still frame reduced motion
    // gets would freeze it at whatever radius it had reached. The water's
    // own answer to a click is a bump in a height field and survives being
    // frozen; a circle does not.
    if (this.reduced()) return;
    if (this.st.pings.length > 6) this.st.pings.shift();
    this.st.pings.push({ x: x, y: y, t0: this.t });
    if (!this.running()) this.draw(this.t);
  };

  Ocean.prototype.running = function () { return !!this.raf; };

  Ocean.prototype.start = function () {
    if (this.raf || !this.visible || this.doc.hidden) return;
    // A frame is already on the canvas from mount; reduced motion means
    // no *loop*, not no picture, so there is nothing more to do.
    if (this.reduced()) return;
    var self = this;
    this.last = 0;
    var loop = function (now) {
      self.raf = self.win.requestAnimationFrame(loop);
      var dt = self.last ? Math.min(0.05, (now - self.last) / 1000) : 0.016;
      self.last = now;
      self.advance(dt);
      self.draw(self.t);
    };
    this.raf = this.win.requestAnimationFrame(loop);
  };

  Ocean.prototype.stop = function () {
    if (!this.raf) return;
    this.win.cancelAnimationFrame(this.raf);
    this.raf = 0;
  };

  Ocean.prototype.advance = function (dt) {
    var st = this.st;
    this.t += dt;
    // Slow attack, slow release: the swell builds as the pointer arrives
    // and settles after it leaves. Water with mass does not snap.
    st.bulge.strength += (st.bulge.target - st.bulge.strength) * Math.min(1, dt * 2.0);
    // Scroll energy is collected by the shared listener and spent here,
    // so the chop builds while you are flicking and settles when you stop.
    st.chop += (Math.min(1, scroll.energy) - st.chop) * Math.min(1, dt * 3);
    scroll.energy *= Math.exp(-dt * 2.2);
    // The descent is chased rather than tracked. A scrollbar drag would
    // otherwise teleport the sea, and water does not teleport; the lag is
    // small enough to read as mass and large enough to smooth a jump.
    st.depth += (scroll.depth - st.depth) * Math.min(1, dt * 3.4);

    var live = [];
    for (var i = 0; i < st.ripples.length; i++) {
      if (this.t - st.ripples[i].t0 <= RIPPLE_LIFE) live.push(st.ripples[i]);
    }
    st.ripples = live;

    // Pings, and what comes back off them. The whale's position is the one
    // the last frame drew, which is a frame stale and invisibly so — the
    // alternative is computing the transform twice per frame for a flash
    // that lasts a fifth of a second.
    var pings = [];
    st.echo *= Math.exp(-dt * ECHO_DECAY);
    for (var k = 0; k < st.pings.length; k++) {
      var p = st.pings[k];
      if (this.t - p.t0 > PING_LIFE) continue;
      pings.push(p);
      if (!this.whalePos) continue;
      var dx = this.whalePos.x - p.x;
      var dy = this.whalePos.y - p.y;
      var band = this.w * ECHO_BAND;
      var miss = Math.abs(pingRadius(p, this.t, this.w) - Math.sqrt(dx * dx + dy * dy));
      if (miss < band) st.echo = Math.max(st.echo, 1 - miss / band);
    }
    st.pings = pings;

    if (this.whale && this.t > st.nextSpout) {
      this.blow();
      st.nextSpout = this.t + 11 + (this.t * 7919 % 9);
    }
    var alive = [];
    for (var j = 0; j < st.spout.length; j++) {
      var p = st.spout[j];
      p.vy += 34 * dt;
      p.x += p.vx * dt;
      p.y += p.vy * dt;
      p.age += dt;
      if (p.age < p.life) alive.push(p);
    }
    st.spout = alive;
  };

  Ocean.prototype.blow = function () {
    var tr = whaleTransform(this.t, this.w, this.h, this.st);
    var o = whaleToScreen(WHALE.blow.x, WHALE.blow.y, tr);
    var scale = tr.span / 900;
    for (var i = 0; i < 16; i++) {
      // Deterministic spread. A plume does not need real randomness and
      // Math.random here would make the DOM test unreproducible.
      var f = (i / 15) * 2 - 1;
      this.st.spout.push({
        x: o.x + f * 4 * scale,
        y: o.y,
        vx: f * 12 * scale,
        vy: (-62 - Math.abs(Math.cos(i * 2.4)) * 38) * scale,
        r: (1.6 + (i % 4) * 0.7) * scale,
        age: 0,
        life: 1.3 + (i % 5) * 0.22,
      });
    }
  };

  // ------------------------------------------------------------- painting

  // Evaluate one layer's surface across the canvas into the reusable
  // buffer. Kept apart from painting it because the fill and the foam
  // line need the same points, and sampling twice would double the only
  // arithmetic in here that is not free.
  Ocean.prototype.sample = function (layer, t) {
    var pts = this.pts;
    var x = 0;
    for (var i = 0; i < this.n; i++) {
      pts[i] = surfaceAt(layer, Math.min(x, this.w), t, this.w, this.h, this.st);
      x += this.step;
    }
  };

  Ocean.prototype.surfacePath = function () {
    var ctx = this.ctx;
    var pts = this.pts;
    ctx.beginPath();
    ctx.moveTo(0, pts[0]);
    var x = this.step;
    for (var i = 1; i < this.n; i++) {
      ctx.lineTo(Math.min(x, this.w), pts[i]);
      x += this.step;
    }
  };

  Ocean.prototype.drawLayer = function (layer, t) {
    var ctx = this.ctx;
    var w = this.w;
    var h = this.h;
    var pal = this.palette;

    this.sample(layer, t);

    // Close the surface polyline down the sides to the floor and fill it.
    this.surfacePath();
    ctx.lineTo(w, h + 2);
    ctx.lineTo(0, h + 2);
    ctx.closePath();

    // The gradient has to start where the surface actually is, not where
    // it rests. Anchored to the resting base instead, a descent fills the
    // whole frame with the *top* of the ramp — canvas clamps to the first
    // stop above it — and the deep comes out brighter than the surface,
    // which is the exact opposite of the thing being drawn.
    var g = ctx.createLinearGradient(0, layerBase(layer, this.st) * h - h * 0.1, 0, h);
    g.addColorStop(0, pal.sea[layer.tint]);
    g.addColorStop(1, pal.deep);
    // A layer that has climbed out of the frame is behind you. It still
    // has to paint — it is what the water between you and it looks like —
    // but four full-strength layers stacked over the whole frame is a
    // wall, not a depth.
    ctx.globalAlpha = layer.alpha * (1 - this.st.depth * 0.45);
    ctx.fillStyle = g;
    ctx.fill();

    // A bright line right on the surface. This is doing more work than
    // anything else here — without it the layers are flat shapes, with it
    // they are water.
    if (layer.foam > 0) {
      this.surfacePath();
      ctx.globalAlpha = layer.foam * (1 - this.st.depth * 0.45);
      ctx.strokeStyle = pal.foam;
      ctx.lineWidth = 1;
      ctx.stroke();
    }
    ctx.globalAlpha = 1;
  };

  // The night sky, drawn first because it is farther away than anything.
  // On the light theme the star colour resolves to transparent and the
  // whole pass is skipped — by day there are no stars, not dim ones.
  Ocean.prototype.drawStars = function (t) {
    var pal = this.palette;
    if (!visibleColour(pal.star)) return;
    // Under water there is no sky. They go out over the first eighth of
    // the descent, which is about one screen on a docs page — long enough
    // to watch happen, short enough that you are not reading a paragraph
    // next to stars you have supposedly left above you.
    var above = 1 - Math.min(1, this.st.depth / 0.12);
    if (above <= 0) return;
    if (!this.stars) {
      // The band's sky is a sliver; the viewport's is half the frame. The
      // clamps come down with it or the strip snows.
      var band = this.layout === 'band';
      this.stars = starField(this.w, this.h, this.sky,
        band ? 4 : 70, band ? 24 : 220);
    }
    var ctx = this.ctx;
    for (var i = 0; i < this.stars.length; i++) {
      var s = this.stars[i];
      var x = s.x * this.w;
      var y = s.y * this.h;
      // Each star breathes on its own slow period. 1 - tw is the floor:
      // a star never blinks out, it dims.
      var twinkle = 1 - s.tw + s.tw * (0.5 + 0.5 * Math.sin(t * s.spd + s.phase));
      // Atmospheric extinction: the sky pales toward the horizon, so the
      // stars go with it instead of sitting on top of it.
      var fade = 1 - (s.y / this.sky) * 0.55;
      var alpha = s.a * twinkle * fade * above;
      ctx.globalAlpha = alpha;
      ctx.fillStyle = s.warm ? pal.starWarm : pal.star;
      ctx.beginPath();
      ctx.arc(x, y, s.r, 0, Math.PI * 2);
      ctx.fill();
      // Only the brightest few get a diffraction glint, and it is two
      // hairlines at a third of the star's own alpha — a hint, not a
      // sparkle sprite.
      if (s.r > 1.7) {
        var g = s.r * 3.2;
        ctx.globalAlpha = alpha * 0.35;
        ctx.strokeStyle = s.warm ? pal.starWarm : pal.star;
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.moveTo(x - g, y);
        ctx.lineTo(x + g, y);
        ctx.moveTo(x, y - g);
        ctx.lineTo(x, y + g);
        ctx.stroke();
      }
    }
    ctx.globalAlpha = 1;
  };

  // Sun glitter, drawn last because it sits on top of the water. Each
  // glint is a short dash lying along the surface that flashes on its own
  // period — cubing the sine keeps it dark most of the time and bright
  // briefly, which is what glitter does; a linear pulse just throbs.
  Ocean.prototype.drawGlitter = function (t) {
    var pal = this.palette;
    if (!visibleColour(pal.sparkle)) return;
    if (!this.glints) this.glints = glitterField(this.w, this.h);
    var ctx = this.ctx;
    ctx.fillStyle = pal.sparkle;
    for (var i = 0; i < this.glints.length; i++) {
      var g = this.glints[i];
      var s = Math.sin(t * g.spd + g.phase);
      if (s <= 0) continue;
      var x = g.x * this.w;
      var y = surfaceAt(this.layers[g.layer], x, t, this.w, this.h, this.st);
      ctx.globalAlpha = g.a * s * s * s;
      ctx.fillRect(x - g.len / 2, y - 0.5, g.len, 1.2);
    }
    ctx.globalAlpha = 1;
  };

  Ocean.prototype.drawWhale = function (t) {
    if (!this.whale || !this.body) return;
    var ctx = this.ctx;
    var pal = this.palette;
    var tr = whaleTransform(t, this.w, this.h, this.st);
    // Kept for the echo test in `advance`, which needs to know where the
    // thing it is pinging actually ended up.
    this.whalePos = { x: tr.x, y: tr.y };

    // Distance dims it: down in the dark it is a suggestion of a shape,
    // not an illustration. `echo` is the answer to a ping, and it is
    // deliberately strongest exactly where the dimming is — the deeper you
    // are, the more of what you see is something you asked for.
    var echo = Math.min(1, this.st.echo);
    var lost = Math.min(1, this.st.depth * 1.15);
    var seen = (1 - lost * 0.72) + echo * (0.25 + lost * 0.7);

    // A soft light behind it, so a dark silhouette on a dark page still
    // has an edge to sit against.
    var g = ctx.createRadialGradient(tr.x, tr.y, 0, tr.x, tr.y, tr.span * 0.62);
    g.addColorStop(0, pal.glow);
    g.addColorStop(1, pal.glowFade);
    ctx.globalAlpha = Math.max(0, Math.min(1, seen));
    ctx.fillStyle = g;
    ctx.fillRect(tr.x - tr.span * 0.7, tr.y - tr.span * 0.7, tr.span * 1.4, tr.span * 1.4);
    ctx.globalAlpha = 1;

    ctx.save();
    ctx.globalAlpha = Math.max(0.05, Math.min(1, seen));
    ctx.translate(tr.x, tr.y);
    ctx.rotate(tr.rot);
    ctx.scale(tr.sx, tr.sy);
    ctx.translate(-WHALE.view.w / 2, -WHALE.view.h * WATERLINE);

    ctx.fillStyle = pal.whale;
    ctx.fill(this.body);

    // The far flipper, a shade darker than the body. Canvas alpha is
    // absolute rather than multiplied into what is already set, so every
    // part that wants to be dimmer than the body has to say `seen` again
    // — otherwise a fading whale keeps a bright flipper and a bright eye,
    // which is a whale wearing jewellery in the dark.
    ctx.globalAlpha = 0.55 * seen;
    ctx.fill(this.flipper);

    // The sonar return: the outline lights up where the ring passed. It is
    // a stroke rather than a brighter fill because that is what coming
    // back off an edge looks like, and because the shape is the
    // information — you are being told *what* is down there, not how
    // brightly it glows. The line width is divided back through the
    // whale's own scale so it stays a hairline on screen at any size.
    if (echo > 0.02) {
      ctx.globalAlpha = Math.min(1, echo * 0.9);
      ctx.strokeStyle = pal.whaleLit;
      ctx.lineWidth = 3 / Math.max(0.001, Math.abs(tr.sx));
      ctx.stroke(this.body);
    }

    // The mouth line and the eye have to be lighter than the body or a
    // silhouette swallows them — and they are most of what makes the
    // head read as a whale's rather than a submarine's.
    if (this.mouth) {
      ctx.strokeStyle = pal.whaleLit;
      ctx.globalAlpha = 0.3;
      ctx.lineWidth = 3;
      ctx.lineCap = 'round';
      ctx.stroke(this.mouth);
    }
    ctx.fillStyle = pal.whaleLit;
    ctx.globalAlpha = Math.min(1, 0.75 * seen + echo * 0.6);
    ctx.beginPath();
    ctx.arc(WHALE.eye.x, WHALE.eye.y, WHALE.eye.r, 0, Math.PI * 2);
    ctx.fill();
    ctx.globalAlpha = 1;
    ctx.restore();
  };

  Ocean.prototype.drawSpout = function () {
    var st = this.st;
    if (!st.spout.length) return;
    var ctx = this.ctx;
    ctx.fillStyle = this.palette.foam;
    for (var i = 0; i < st.spout.length; i++) {
      var p = st.spout[i];
      // Mist, not popcorn: faint, and it spreads as it ages.
      ctx.globalAlpha = 0.34 * (1 - p.age / p.life);
      ctx.beginPath();
      ctx.arc(p.x, p.y, p.r * (1 + p.age * 0.7), 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;
  };

  // The water you are inside, as opposed to the water you are looking at.
  // Once the surface has climbed out of the frame the layers stop covering
  // the bottom of it, and without this the deep is just the page
  // background — which is to say, nothing happened.
  //
  // It is a gradient rather than a flat wash because the light in real
  // water comes from above: even at the bottom, up is brighter than down.
  Ocean.prototype.drawAbyss = function () {
    var d = this.st.depth;
    if (d <= 0.01) return;
    var ctx = this.ctx;
    var g = ctx.createLinearGradient(0, 0, 0, this.h);
    // Not transparent at the top: at full depth the dark is *everywhere*,
    // and a veil that fades out upward reads as a surface just overhead —
    // which is the one thing you are supposed to have left behind. The
    // gradient that remains is the light still coming from above.
    g.addColorStop(0, withAlpha(this.palette.abyss, 0.5));
    g.addColorStop(1, this.palette.abyss);
    ctx.globalAlpha = Math.min(1, d * 1.25);
    ctx.fillStyle = g;
    ctx.fillRect(0, 0, this.w, this.h);
    ctx.globalAlpha = 1;
  };

  // Marine snow, in front of the water because it is between you and it.
  Ocean.prototype.drawSnow = function (t) {
    var d = this.st.depth;
    if (d <= SNOW_START) return;
    var pal = this.palette;
    if (!visibleColour(pal.foam)) return;
    if (!this.snow) this.snow = snowField(this.w, this.h);
    var strength = Math.min(1, (d - SNOW_START) / (SNOW_FULL - SNOW_START));
    var ctx = this.ctx;
    ctx.fillStyle = pal.foam;
    for (var i = 0; i < this.snow.length; i++) {
      var s = this.snow[i];
      // Depth moves it up (that is you, going down); time moves it down
      // (that is the snow, falling). Wrapping on 1 keeps the field
      // seamless without dealing new flecks at the edges.
      var y = s.y - d * SNOW_RISE * s.near + t * s.sink;
      y = y - Math.floor(y);
      var x = s.x + s.drift * (d * SNOW_RISE * s.near) + Math.sin(t * 0.2 + s.x * 9) * 0.004;
      x = x - Math.floor(x);
      ctx.globalAlpha = s.a * strength;
      ctx.beginPath();
      ctx.arc(x * this.w, y * this.h, s.r, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;
  };

  // The pulse itself. Two arcs: the ring, and a fainter one just behind it
  // for the trail. Both thin — a sonar sweep is a line, and a thick soft
  // ring reads as a button's ripple, which is the wrong idea entirely.
  Ocean.prototype.drawPings = function (t) {
    var st = this.st;
    if (!st.pings.length) return;
    var ctx = this.ctx;
    var pal = this.palette;
    for (var i = 0; i < st.pings.length; i++) {
      var p = st.pings[i];
      var age = t - p.t0;
      if (age < 0) continue;
      var r = pingRadius(p, t, this.w);
      if (r <= 0) continue;
      // Squared, so it dies out of sight rather than switching off — the
      // same fade the water ripple uses, for the same reason.
      var fade = 1 - age / PING_LIFE;
      fade = fade * fade;
      ctx.strokeStyle = pal.foam;
      ctx.lineWidth = 1;
      ctx.globalAlpha = 0.5 * fade;
      ctx.beginPath();
      ctx.arc(p.x, p.y, r, 0, Math.PI * 2);
      ctx.stroke();
      if (r > this.w * 0.06) {
        ctx.globalAlpha = 0.16 * fade;
        ctx.beginPath();
        ctx.arc(p.x, p.y, r - this.w * 0.045, 0, Math.PI * 2);
        ctx.stroke();
      }
    }
    ctx.globalAlpha = 1;
  };

  Ocean.prototype.draw = function (t) {
    if (!this.w || !this.h) return;
    var ctx = this.ctx;
    ctx.clearRect(0, 0, this.w, this.h);
    this.drawStars(t);
    for (var i = 0; i < this.layers.length; i++) {
      if (i === WHALE_LAYER) {
        this.drawWhale(t);
        this.drawSpout();
      }
      this.drawLayer(this.layers[i], t);
    }
    // Order is the whole argument for where these four go. Glitter is
    // light lying on the water, so it belongs with the water and ahead of
    // the dark that has to be able to swallow it; the dark is between you
    // and the water; the snow is between you and the dark; and the ping is
    // not in the water at all — it is the instrument reading, so it sits
    // on top of everything.
    this.drawGlitter(t);
    this.drawAbyss();
    this.drawSnow(t);
    this.drawPings(t);
    this.frames++;
  };

  Ocean.prototype.destroy = function () {
    this.stop();
    var win = this.win;
    if (win.removeEventListener) {
      win.removeEventListener('pointermove', this.onMove);
      win.removeEventListener('pointerdown', this.onDown);
      win.removeEventListener('blur', this.onLeave);
      if (this.onResize) win.removeEventListener('resize', this.onResize);
    }
    if (this.onVis && this.doc.removeEventListener) {
      this.doc.removeEventListener('visibilitychange', this.onVis);
    }
    if (this.ro) this.ro.disconnect();
    if (this.io) this.io.disconnect();
    if (this.mo) this.mo.disconnect();
    if (this.canvas.remove) this.canvas.remove();
  };

  // -------------------------------------------------------------- controls

  // The same gesture, on the things that are genuinely clickable. A click
  // on the page pings the water; a click on a control pings from the
  // control. Without this second half the page has two click languages —
  // one for the scenery and one for the buttons — and the scenery's is the
  // better one.
  //
  // The ring itself is CSS, on a pseudo-element of the control, so it
  // starts at the control's own edge rather than somewhere behind it. All
  // this does is start it: add a class, take it off when the animation
  // ends. A second click mid-animation has to see the class removed and
  // re-added or nothing restarts, which is what the reflow read is for.
  var SEEKABLE = '.btn, .sitenav a, .themetoggle, .pager a, .pg-primary';

  function wireSeek(doc) {
    if (doc.dsSeekWired || !doc.addEventListener) return;
    doc.dsSeekWired = true;
    var win = doc.defaultView || global;
    doc.addEventListener('pointerdown', function (e) {
      if (win.matchMedia && win.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
      var el = e.target && e.target.closest && e.target.closest(SEEKABLE);
      if (!el || !el.classList) return;
      el.classList.remove('is-seeking');
      // Reading a layout property forces the class removal to take effect
      // before it goes back on, which is what restarts the animation.
      void el.offsetWidth;
      el.classList.add('is-seeking');
    }, { passive: true, capture: true });
    doc.addEventListener('animationend', function (e) {
      if (e.animationName === 'seek-out' && e.target.classList) {
        e.target.classList.remove('is-seeking');
      }
    }, true);
  }

  // ----------------------------------------------------------------- mount

  function mount(el, opts) {
    if (!el || el.dataset && el.dataset.oceanMounted) return null;
    if (el.dataset) el.dataset.oceanMounted = '1';
    return new Ocean(el, opts);
  }

  // Every ocean on the page is a `<div data-ocean>`; the data attributes
  // carry the two things that differ between them. The div also carries a
  // CSS gradient and a static whale, so a reader with JS off still gets a
  // sea — this only ever upgrades what is already there.
  // Every ocean that has been mounted, in page order. The script starts
  // itself, so this is the only handle on the instances — for the tests,
  // and for anyone poking at DSWaves.mounted[0] in a console.
  var mounted = [];

  function auto(doc) {
    var out = [];
    wireSeek(doc);
    var hosts = doc.querySelectorAll('[data-ocean]');
    for (var i = 0; i < hosts.length; i++) {
      var el = hosts[i];
      var o = mount(el, {
        layout: el.getAttribute('data-ocean') === 'band' ? 'band' : 'viewport',
        whale: el.getAttribute('data-whale') !== 'off',
        whaleX: parseFloat(el.getAttribute('data-whale-x')) || undefined,
        whaleSpan: parseFloat(el.getAttribute('data-whale-span')) || undefined,
        facing: el.getAttribute('data-facing') || 'left',
      });
      if (o) {
        el.classList.add('is-live');
        mounted.push(o);
        out.push(o);
      }
    }
    return out;
  }

  var api = {
    mount: mount,
    auto: auto,
    mounted: mounted,
    Ocean: Ocean,
    WHALE: WHALE,
    MARK: MARK,
    LAYERS: LAYERS,
    BAND_LAYERS: BAND_LAYERS,
    PROFILES: PROFILES,
    WHALE_LAYER: WHALE_LAYER,
    WATERLINE: WATERLINE,
    RIPPLE_LIFE: RIPPLE_LIFE,
    SLOPE_SPAN: SLOPE_SPAN,
    TILT_GAIN: TILT_GAIN,
    TILT_MAX: TILT_MAX,
    SKY: SKY,
    DIVE_RISE: DIVE_RISE,
    WHALE_LAG: WHALE_LAG,
    SNOW_START: SNOW_START,
    SNOW_FULL: SNOW_FULL,
    PING_LIFE: PING_LIFE,
    PING_SPEED: PING_SPEED,
    FLOOR_M: FLOOR_M,
    scroll: scroll,
    starRand: starRand,
    starField: starField,
    glitterField: glitterField,
    snowField: snowField,
    visibleColour: visibleColour,
    waveAt: waveAt,
    rippleAt: rippleAt,
    bulgeAt: bulgeAt,
    surfaceAt: surfaceAt,
    layerBase: layerBase,
    pingRadius: pingRadius,
    depthOf: depthOf,
    publishDepth: publishDepth,
    wireSeek: wireSeek,
    SEEKABLE: SEEKABLE,
    whaleTransform: whaleTransform,
    whaleToScreen: whaleToScreen,
    readPalette: readPalette,
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  global.DSWaves = api;

  if (typeof document !== 'undefined' && document.querySelectorAll) {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', function () { auto(document); });
    } else {
      auto(document);
    }
  }
})(typeof self !== 'undefined' ? self : this);
