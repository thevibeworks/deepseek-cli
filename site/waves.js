// Live water, and a whale in it.
//
// Four wave layers, each a sum of three sines, drawn back to front onto a
// 2D canvas. The whale is filled between layer 1 and layer 2, so the two
// nearest layers wash over it — that draw order, and nothing else, is what
// makes it read as *in* the water rather than pasted on top of it.
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

  // A blue whale, facing left, in a 1000x430 box. Hand-authored: blunt
  // rostrum, the long taper to a thin tail stock, the small swept dorsal
  // fin set three-quarters back, and the fluke drawn side-on the way
  // every whale mark since Moby-Dick has cheated it — a true side view
  // would show the fluke edge-on as a line, and nobody would read it as
  // a whale.
  //
  // The flipper is a second path rather than part of the body because it
  // overlaps it. Filled together under the nonzero rule the overlap
  // cancels and punches a hole in the belly; filled separately it can
  // also take a darker shade, which reads as the far flipper and gives
  // the silhouette its only hint of depth.
  var WHALE = {
    view: { w: 1000, h: 430 },
    body: 'M22 222C40 186 96 158 178 150C268 141 352 142 430 150'
        + 'C560 163 654 180 716 194C726 176 738 160 756 150'
        + 'C764 168 766 186 764 203C784 207 797 210 808 212'
        + 'C840 186 898 148 986 116C946 158 902 194 872 219'
        + 'C902 246 946 284 986 328C898 292 840 250 808 224'
        + 'C782 233 750 242 714 250C640 268 556 284 452 292'
        + 'C348 300 262 300 186 292C100 282 42 258 22 222Z',
    fin: 'M232 278C282 280 328 302 366 344C392 372 410 400 424 416'
       + 'C406 408 380 390 342 358C306 328 268 300 232 278Z',
    eye: { x: 104, y: 236, r: 9 },
    blow: { x: 196, y: 149 },
  };

  // Where the resting waterline crosses the whale, as a fraction of the
  // box height. The body runs from y=150 to y=292 amidships, so 0.55 —
  // y=236 — leaves about three-fifths of it showing: back, head and the
  // upper fluke lobe above water, belly and flipper under it. Lower than
  // this and the silhouette stops being readable as a whale; higher and
  // it stops looking like it is in the water at all.
  var WATERLINE = 0.55;

  // ---------------------------------------------------------------- waves

  // Farthest first. `base` is the resting waterline as a fraction of the
  // canvas height, so every layer is one horizon further down the frame.
  // Nearer layers are faster, choppier and more opaque; `reach` is how
  // much of the pointer and click energy they pick up, which is the same
  // gradient — a swell on the horizon does not care where your mouse is.
  //
  // Speeds alternate sign so the layers slide across each other instead
  // of marching in step, which is most of what sells the parallax.
  var LAYERS = [
    { base: 0.30, amp: 0.058, crests: 1.5, speed: 0.19, reach: 0.28, alpha: 0.30, foam: 0.00, tint: 0 },
    { base: 0.42, amp: 0.050, crests: 2.3, speed: -0.27, reach: 0.52, alpha: 0.40, foam: 0.16, tint: 1 },
    { base: 0.55, amp: 0.043, crests: 3.2, speed: 0.36, reach: 0.78, alpha: 0.52, foam: 0.36, tint: 2 },
    { base: 0.72, amp: 0.036, crests: 4.5, speed: -0.47, reach: 1.00, alpha: 0.72, foam: 0.60, tint: 3 },
  ];

  // The whale is drawn immediately before this layer, so everything from
  // here down is in front of it and LAYERS[WHALE_LAYER] is the surface it
  // rides.
  var WHALE_LAYER = 2;

  var RIPPLE_LIFE = 2.8;    // seconds until a click is fully forgotten
  var RIPPLE_SPEED = 0.52;  // canvas widths per second
  var RIPPLE_WIDTH = 0.07;  // packet envelope, as a fraction of width
  var BULGE_WIDTH = 0.13;   // pointer hill, as a fraction of width
  var BULGE_HEIGHT = 0.05;  // ... and its peak, as a fraction of height

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
    return r.amp * fade * fade * env * Math.cos((travel * 2 * Math.PI) / (sigma * 1.15));
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
  // the surface through this, including the whale.
  function surfaceAt(layer, x, t, w, h, st) {
    var y = layer.base * h + waveAt(layer, x, t, w) * h * (1 + st.chop * 1.6);
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
  var TILT_GAIN = 0.85;   // ... and how much of that slope it takes on
  var TILT_MAX = 0.12;    // ~7 degrees, past which it stops reading as calm

  // Where the whale sits this frame, and how far over it is leaning. Both
  // come out of the height field rather than a clock: the tilt is the
  // slope of the surface it is floating on, sampled either side of it.
  function whaleTransform(t, w, h, st) {
    var narrow = w < NARROW;
    var frac = narrow ? Math.min(0.94, st.whaleSpan + 0.32) : st.whaleSpan;
    var span = Math.min(Math.max(w * frac, 260), 1200);
    var sx = span / WHALE.view.w;
    var at = narrow ? 0.5 : st.whaleX;
    var x = w * at + Math.sin(t * 0.11) * w * (narrow ? 0.01 : 0.02);
    var lay = LAYERS[WHALE_LAYER];
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
      y: y + Math.sin(t * 0.37 + 1.2) * h * 0.006,
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
  function readPalette(read) {
    var sea = PALETTE_KEYS.map(function (k) { return read(k) || '#00c2e9'; });
    return {
      sea: sea,
      deep: read('--sea-deep') || '#00131c',
      foam: read('--sea-foam') || '#ffffff',
      whale: read('--whale') || '#0b2f4a',
      whaleLit: read('--whale-lit') || '#00c2e9',
      glow: read('--sea-glow') || 'rgba(0,194,233,0.12)',
      // The far end of the glow has to be the same hue at zero alpha, not
      // the keyword `transparent` — that is transparent *black*, and a
      // gradient running to it puts a grey halo round the whale on the
      // light theme.
      glowFade: read('--sea-glow-fade') || 'rgba(0,194,233,0)',
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
      scroll.energy = Math.min(1, scroll.energy + dv / 260);
    }, { passive: true });
  }

  function Ocean(host, opts) {
    opts = opts || {};
    var doc = host.ownerDocument;
    var win = doc.defaultView || global;

    var canvas = doc.createElement('canvas');
    canvas.className = 'ocean-canvas';
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

    this.st = {
      chop: 0,
      ripples: [],
      spout: [],
      bulge: { x: 0, strength: 0, target: 0 },
      whaleX: opts.whaleX != null ? opts.whaleX : 0.62,
      whaleSpan: opts.whaleSpan != null ? opts.whaleSpan : 0.62,
      facing: opts.facing === 'right' ? -1 : 1,
      nextSpout: 6,
    };
    this.whale = opts.whale !== false;

    this.body = global.Path2D ? new global.Path2D(WHALE.body) : null;
    this.flipper = global.Path2D ? new global.Path2D(WHALE.fin) : null;

    this.palette = readPalette(function () { return ''; });
    this.refreshPalette();
    this.resize();
    this.wire();
    this.start();
  }

  Ocean.prototype.refreshPalette = function () {
    var win = this.win;
    var el = this.doc.documentElement;
    if (!win.getComputedStyle) return;
    var cs = win.getComputedStyle(el);
    this.palette = readPalette(function (k) {
      return (cs.getPropertyValue(k) || '').trim();
    });
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
    return true;
  };

  Ocean.prototype.wire = function () {
    var self = this;
    var win = this.win;
    wireScroll(win);

    // The canvas is pointer-events: none so it never shadows a link, so
    // the pointer has to be tracked on the window and mapped in. The side
    // effect is the right one: the water responds to the pointer anywhere
    // on the page, not only where it happens to be painted.
    this.onMove = function (e) {
      var r = self.canvas.getBoundingClientRect();
      self.st.bulge.x = e.clientX - r.left;
      var near = e.clientY > r.top - 400 && e.clientY < r.bottom + 400;
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
    this.st.ripples.push({ x: x, t0: this.t, amp: (amp || 1) * this.h * 0.055 });
    if (!this.running()) this.draw(this.t);
    // Clicking near the whale startles it into blowing.
    if (this.whale && Math.abs(x - this.w * this.st.whaleX) < this.w * 0.3) {
      this.st.nextSpout = Math.min(this.st.nextSpout, this.t + 0.25);
    }
  };

  Ocean.prototype.running = function () { return !!this.raf; };

  Ocean.prototype.start = function () {
    if (this.raf || !this.visible || this.doc.hidden) return;
    if (this.reduced()) { this.draw(0); return; }
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
    st.bulge.strength += (st.bulge.target - st.bulge.strength) * Math.min(1, dt * 4.5);
    // Scroll energy is collected by the shared listener and spent here,
    // so the chop builds while you are flicking and settles when you stop.
    st.chop += (Math.min(1, scroll.energy) - st.chop) * Math.min(1, dt * 5);
    scroll.energy *= Math.exp(-dt * 2.6);

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
      p.vy += 9.4 * dt;
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
    for (var i = 0; i < 22; i++) {
      // Deterministic spread. A plume does not need real randomness and
      // Math.random here would make the DOM test unreproducible.
      var f = (i / 21) * 2 - 1;
      this.st.spout.push({
        x: o.x + f * 5 * scale,
        y: o.y,
        vx: f * 26 * scale,
        vy: (-96 - Math.abs(Math.cos(i * 2.4)) * 54) * scale,
        r: (2.2 + (i % 4) * 0.9) * scale,
        age: 0,
        life: 1.1 + (i % 5) * 0.16,
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
      ctx.lineWidth = 1.4;
      ctx.stroke();
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

    // The eye has to be lighter than the body or a silhouette swallows it.
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
      ctx.globalAlpha = 0.5 * (1 - p.age / p.life);
      ctx.beginPath();
      ctx.arc(p.x, p.y, p.r, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;
  };

  Ocean.prototype.draw = function (t) {
    if (!this.w || !this.h) return;
    var ctx = this.ctx;
    ctx.clearRect(0, 0, this.w, this.h);
    for (var i = 0; i < LAYERS.length; i++) {
      if (i === WHALE_LAYER) {
        this.drawWhale(t);
        this.drawSpout();
      }
      this.drawLayer(LAYERS[i], t);
    }
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
    LAYERS: LAYERS,
    WHALE_LAYER: WHALE_LAYER,
    WATERLINE: WATERLINE,
    RIPPLE_LIFE: RIPPLE_LIFE,
    SLOPE_SPAN: SLOPE_SPAN,
    TILT_GAIN: TILT_GAIN,
    TILT_MAX: TILT_MAX,
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
