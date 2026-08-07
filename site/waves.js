// Live water, a whale in it, and at night a sky of stars over it.
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

  // The site's mark: the sounding fluke, in a 32x32 box with the
  // waterline at y≈25. A whale lifts its tail exactly once per dive, and
  // that gesture — down into the deep — is the closest one picture gets
  // to the name DeepSeek. It is not the illustration shrunk: a mark that
  // reads at 16 pixels needs blades this bold, and the illustration's
  // fluke would be a sliver. The favicon and the masthead carry this
  // path verbatim; waves.test.js pins both, because nobody reviews a
  // favicon and a drifted mark would never be noticed by looking.
  var MARK = 'M5.6 8.2C4 7 2.4 7.6 3.3 9.5C5.2 13.7 8.7 17.8 12.1 20.7'
    + 'C13.3 21.8 14.1 23.3 14.6 24.9C15.2 23.5 16 22.8 17 22.4'
    + 'C18.1 22.7 19.1 23.5 19.9 24.6C20.7 22.9 21.6 21.2 23 19.5'
    + 'C25.4 16.5 27.9 13.6 29.2 10.5C30 8.5 28.4 7.5 26.7 8.7'
    + 'C23 11.4 19.3 14.9 16.9 18.4C14.3 14.7 9.9 10.5 5.6 8.2Z';

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
    var y = layer.base * h + waveAt(layer, x, t, w) * h * (1 + st.chop * 0.7);
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

  // One scroll listener for the whole page. Every ocean on the page reads
  // the same energy, so scrolling roughs up all of them together — which
  // is what one body of water would do.
  var scroll = { last: 0, energy: 0, wired: false };

  function wireScroll(win) {
    if (scroll.wired || !win.addEventListener) return;
    scroll.wired = true;
    scroll.last = win.pageYOffset || 0;
    win.addEventListener('scroll', function () {
      var now = win.pageYOffset || 0;
      var dv = Math.abs(now - scroll.last);
      scroll.last = now;
      // Capped well under 1: a flick should stir the water, not turn it
      // to static. At the old ceiling a fast scroll doubled the swell
      // amplitude under the text you were trying to read.
      scroll.energy = Math.min(0.45, scroll.energy + dv / 600);
    }, { passive: true });
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
      ripples: [],
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
    // The star and glitter counts follow the frame's area, so a resize
    // deals both fields again from the same seeds.
    this.stars = null;
    this.glints = null;
    return true;
  };

  Ocean.prototype.wire = function () {
    var self = this;
    var win = this.win;
    wireScroll(win);

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

    var live = [];
    for (var i = 0; i < st.ripples.length; i++) {
      if (this.t - st.ripples[i].t0 <= RIPPLE_LIFE) live.push(st.ripples[i]);
    }
    st.ripples = live;

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

    var g = ctx.createLinearGradient(0, layer.base * h - h * 0.1, 0, h);
    g.addColorStop(0, pal.sea[layer.tint]);
    g.addColorStop(1, pal.deep);
    ctx.globalAlpha = layer.alpha;
    ctx.fillStyle = g;
    ctx.fill();

    // A bright line right on the surface. This is doing more work than
    // anything else here — without it the layers are flat shapes, with it
    // they are water.
    if (layer.foam > 0) {
      this.surfacePath();
      ctx.globalAlpha = layer.foam;
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
      var alpha = s.a * twinkle * fade;
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

    // A soft light behind it, so a dark silhouette on a dark page still
    // has an edge to sit against.
    var g = ctx.createRadialGradient(tr.x, tr.y, 0, tr.x, tr.y, tr.span * 0.62);
    g.addColorStop(0, pal.glow);
    g.addColorStop(1, pal.glowFade);
    ctx.fillStyle = g;
    ctx.fillRect(tr.x - tr.span * 0.7, tr.y - tr.span * 0.7, tr.span * 1.4, tr.span * 1.4);

    ctx.save();
    ctx.translate(tr.x, tr.y);
    ctx.rotate(tr.rot);
    ctx.scale(tr.sx, tr.sy);
    ctx.translate(-WHALE.view.w / 2, -WHALE.view.h * WATERLINE);

    ctx.fillStyle = pal.whale;
    ctx.fill(this.body);

    // The far flipper, a shade darker than the body.
    ctx.globalAlpha = 0.55;
    ctx.fill(this.flipper);
    ctx.globalAlpha = 1;

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
    ctx.globalAlpha = 0.75;
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
    this.drawGlitter(t);
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
    starRand: starRand,
    starField: starField,
    glitterField: glitterField,
    visibleColour: visibleColour,
    waveAt: waveAt,
    rippleAt: rippleAt,
    bulgeAt: bulgeAt,
    surfaceAt: surfaceAt,
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
