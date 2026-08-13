// The wave engine, driven without a browser.
//
// waves.test.js checks the arithmetic. This checks the things that only
// go wrong once there is a canvas in front of it: the draw order that
// puts the whale *inside* the water rather than on top of it, and the
// four separate reasons the animation is supposed to stop. A loop that
// keeps running in a hidden tab is not visible in review, is not visible
// on the page, and is visible on a laptop battery.
//
// The 2D context is a recorder. Every call lands in a list with the
// save/restore depth it happened at, which is enough to tell the whale
// (drawn inside a transform) from the water (drawn outside one) – and to
// catch a missing restore, which would leak the whale's rotation onto
// every layer in front of it.
//
//   node site/waves.dom.test.js

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const here = __dirname;
const SRC = fs.readFileSync(path.join(here, 'waves.js'), 'utf8');

let failures = 0;

function check(name, ok, detail) {
  if (ok) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ': ' + detail : ''}`);
    failures++;
  }
}

// --- a 2D context that remembers what it was asked to draw --------------

// A real CanvasGradient throws on a colour it cannot parse, and a real
// fillStyle silently ignores one. The first version of this shim accepted
// anything, so it happily "painted" the string
// "light-dark(#bfe4ef, #0a4d63)" – which is what getComputedStyle hands
// back for every token on this site, because custom properties are
// substituted lazily and nothing resolves light-dark() until it is used in
// a real property. Every frame threw in the browser and the page shipped
// with a blank hole in it. The shim is strict now; that is the fix that
// keeps it fixed.
const COLOUR = /^(#[0-9a-f]{3,8}|rgba?\([^)]*\)|hsla?\([^)]*\)|transparent|[a-z]+)$/i;

function parseableColour(v) {
  return typeof v === 'string' && COLOUR.test(v.trim()) && !/\bvar\(|light-dark\(|color-mix\(/i.test(v);
}

function makeCtx() {
  const ops = [];
  let depth = 0;
  const rec = (op, args) => ops.push({ op, depth, args: args || [] });
  const gradient = {
    addColorStop(_, c) {
      if (!parseableColour(c)) {
        throw new Error(
          "Failed to execute 'addColorStop' on 'CanvasGradient': " +
          `The value provided ('${c}') could not be parsed as a color.`);
      }
    },
  };
  return {
    ops,
    fillStyle: '',
    strokeStyle: '',
    globalAlpha: 1,
    lineWidth: 1,
    save() { rec('save'); depth++; },
    restore() { depth--; rec('restore'); },
    setTransform(...a) { rec('setTransform', a); },
    translate(...a) { rec('translate', a); },
    rotate(...a) { rec('rotate', a); },
    scale(...a) { rec('scale', a); },
    clearRect(...a) { rec('clearRect', a); },
    beginPath() { rec('beginPath'); },
    moveTo(...a) { rec('moveTo', a); },
    lineTo(...a) { rec('lineTo', a); },
    closePath() { rec('closePath'); },
    arc(...a) { rec('arc', a); },
    fill(p) { rec(p ? 'fillPath' : 'fill', p ? [p.d] : []); },
    stroke() { rec('stroke'); },
    fillRect(...a) { rec('fillRect', a); },
    createLinearGradient() { return gradient; },
    createRadialGradient() { return gradient; },
    get depth() { return depth; },
  };
}

// --- the smallest DOM the engine will accept ----------------------------

function makeEl(tag, doc) {
  const el = {
    tagName: tag,
    ownerDocument: doc,
    className: '',
    dataset: {},
    attrs: {},
    style: {},
    children: [],
    width: 0,
    height: 0,
    box: { width: 1200, height: 400, left: 0, top: 0, right: 1200, bottom: 400 },
    classList: {
      added: [],
      add(c) { if (this.added.indexOf(c) < 0) this.added.push(c); },
      remove(c) { this.added = this.added.filter((x) => x !== c); },
      contains(c) { return this.added.indexOf(c) >= 0; },
    },
    textContent: '',
    offsetWidth: 0,
    closest(sel) {
      // Enough of a match for the seek test: the class list against a
      // comma-separated list of class selectors, walking up parents.
      const want = sel.split(',').map((s) => s.trim().replace(/^\./, ''));
      let node = el;
      while (node) {
        const have = String(node.className).split(/\s+/);
        if (want.some((w) => have.indexOf(w) >= 0)) return node;
        node = node.parent;
      }
      return null;
    },
    querySelector(sel) {
      const want = sel.replace(/^\./, '');
      return this.children.filter((c) => String(c.className).split(/\s+/).indexOf(want) >= 0)[0] || null;
    },
    getContext(kind) { return kind === '2d' ? (this.ctx = this.ctx || makeCtx()) : null; },
    getBoundingClientRect() { return this.box; },
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
    setAttribute(k, v) { this.attrs[k] = v; },
    appendChild(c) { this.children.push(c); c.parent = this; return c; },
    remove() { if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this); },
    get clientWidth() { return this.box.width; },
    get clientHeight() { return this.box.height; },
  };
  return el;
}

function makeWorld(opts) {
  opts = opts || {};
  const listeners = { window: {}, document: {} };
  const rafs = [];
  const observers = { resize: [], intersect: [], mutate: [] };

  const doc = {
    hidden: false,
    readyState: 'complete',
    documentElement: null,
    oceans: [],
    gauges: [],
    createElement(tag) { return makeEl(tag, doc); },
    // The page holds two kinds of thing waves.js goes looking for, and
    // they are not the same list – handing the oceans back for every
    // selector would have the gauge lookup find canvases.
    querySelectorAll(sel) {
      return String(sel).indexOf('depth-gauge') >= 0 ? doc.gauges : doc.oceans;
    },
    addEventListener(k, fn) { (listeners.document[k] = listeners.document[k] || []).push(fn); },
    removeEventListener(k, fn) {
      listeners.document[k] = (listeners.document[k] || []).filter((f) => f !== fn);
    },
    fire(k, ev) { (listeners.document[k] || []).forEach((fn) => fn(ev || {})); },
  };
  doc.documentElement = makeEl('html', doc);
  doc.documentElement.scrollHeight = opts.pageHeight || 0;
  doc.documentElement.style.setProperty = function (k, v) { this[k] = v; };
  doc.defaultView = null;

  const sandbox = {
    document: doc,
    console,
    Math,
    JSON,
    Float64Array,
    parseFloat,
    isNaN,
    String,
    Number,
    Set,
    Object,
    devicePixelRatio: opts.dpr === undefined ? 2 : opts.dpr,
    Path2D: function Path2D(d) { this.d = d; },
    requestAnimationFrame(fn) { rafs.push(fn); return rafs.length; },
    cancelAnimationFrame(id) { rafs[id - 1] = null; },
    matchMedia(q) {
      return {
        matches: q.indexOf('reduced-motion') >= 0 ? !!opts.reducedMotion : false,
        addEventListener() {},
      };
    },
    // Faithful to the browser on the one point that matters: reading a
    // custom property gives you the *specified* text, light-dark() and
    // all. Only a real property resolves it. waves.js has to go through
    // the second path or it gets a string canvas cannot paint.
    getComputedStyle(el) {
      return {
        getPropertyValue(k) { return (opts.raw && opts.raw[k]) || ''; },
        get color() {
          // Greedy to the final paren: a fallback is often itself an
          // rgba(), so stopping at the first ')' truncates it.
          const m = /^var\((--[a-z0-9-]+),\s*(.*)\)$/i.exec(
            el && el.style ? String(el.style.color).trim() : '');
          if (!m) return 'rgb(0, 0, 0)';
          const resolved = (opts.palette && opts.palette[m[1]]) || m[2];
          return resolved;
        },
      };
    },
    addEventListener(k, fn) { (listeners.window[k] = listeners.window[k] || []).push(fn); },
    removeEventListener(k, fn) {
      listeners.window[k] = (listeners.window[k] || []).filter((f) => f !== fn);
    },
    pageYOffset: 0,
    innerHeight: opts.innerHeight || 0,
  };
  if (!opts.noObservers) {
    sandbox.ResizeObserver = function (fn) {
      observers.resize.push(fn);
      this.observe = () => {};
      this.disconnect = () => { observers.resize = observers.resize.filter((f) => f !== fn); };
    };
    sandbox.IntersectionObserver = function (fn) {
      observers.intersect.push(fn);
      this.observe = () => {};
      this.disconnect = () => { observers.intersect = observers.intersect.filter((f) => f !== fn); };
    };
    sandbox.MutationObserver = function (fn) {
      observers.mutate.push(fn);
      this.observe = () => {};
      this.disconnect = () => { observers.mutate = observers.mutate.filter((f) => f !== fn); };
    };
  }
  sandbox.self = sandbox;
  sandbox.window = sandbox;
  doc.defaultView = sandbox;

  const world = {
    doc,
    sandbox,
    listeners,
    rafs,
    observers,
    ocean(attrs) {
      const el = makeEl('div', doc);
      el.attrs = attrs || {};
      doc.oceans.push(el);
      return el;
    },
    // A depth gauge, shaped like the one build.py emits: a rail with a
    // fill in it, and a reading the descent writes into.
    gauge() {
      const el = makeEl('div', doc);
      const read = makeEl('span', doc);
      read.className = 'gauge-read';
      el.appendChild(read);
      doc.gauges.push(el);
      return el;
    },
    // Scroll to a fraction of the page and let the shared listener see it.
    scrollTo(fraction) {
      const range = (opts.pageHeight || 0) - (opts.innerHeight || 0);
      sandbox.pageYOffset = Math.round(range * fraction);
      world.fireWindow('scroll');
    },
    // Run one animation frame. The engine re-queues itself from inside
    // the callback, so drain a snapshot rather than the live list.
    frame(now) {
      const due = rafs.splice(0, rafs.length).filter(Boolean);
      due.forEach((fn) => fn(now));
      return due.length;
    },
    fireWindow(k, ev) { (listeners.window[k] || []).forEach((fn) => fn(ev || {})); },
    load() {
      vm.createContext(sandbox);
      vm.runInContext(SRC, sandbox, { filename: 'waves.js' });
      return sandbox.DSWaves;
    },
  };
  return world;
}

// --- draw order ---------------------------------------------------------

console.log('draw order');

{
  const world = makeWorld();
  const host = world.ocean({});
  const W = world.load();

  // The script mounts itself on load; nothing on the page calls it.
  check('the ocean mounts itself', W.mounted.length === 1);
  check('it puts a canvas in the host', host.children.length === 1);
  check('the canvas is hidden from screen readers',
    host.children[0].attrs['aria-hidden'] === 'true');
  check('the host is marked live so the CSS fallback stands down',
    host.classList.added.indexOf('is-live') >= 0);
  check('a second pass over the same page mounts nothing again',
    W.auto(world.doc).length === 0);

  const sea = W.mounted[0];
  check('mounting paints immediately, before any animation frame',
    sea.frames === 1, String(sea.frames));
  sea.canvas.ctx.ops.length = 0;   // measure one frame, not mount plus one
  world.frame(16);
  const ops = sea.canvas.ctx.ops;

  const layerFills = ops.filter((o) => o.op === 'fill' && o.depth === 0);
  check('one fill per wave layer', layerFills.length === W.LAYERS.length,
    String(layerFills.length));

  const foamy = W.LAYERS.filter((l) => l.foam > 0).length;
  check('a foam line on every layer that asks for one',
    ops.filter((o) => o.op === 'stroke' && o.depth === 0).length === foamy);

  // The whale is filled from Path2D inside a save/restore, which is what
  // separates it from the layers in this recording.
  const bodyAt = ops.findIndex((o) => o.op === 'fillPath' && o.args[0] === W.WHALE.body);
  const finAt = ops.findIndex((o) => o.op === 'fillPath' && o.args[0] === W.WHALE.fin);
  check('the whale is drawn', bodyAt >= 0 && finAt > bodyAt);
  check('inside its own transform',
    ops[bodyAt].depth === 1 && ops[finAt].depth === 1);

  // This is the whole illusion: two layers behind it, two in front.
  const before = ops.filter((o, i) => i < bodyAt && o.op === 'fill' && o.depth === 0).length;
  check('with water behind it', before === W.WHALE_LAYER, String(before));
  check('and water in front of it',
    layerFills.length - before === W.LAYERS.length - W.WHALE_LAYER);

  check('the eye is drawn over the body',
    ops.findIndex((o) => o.op === 'arc' && o.depth === 1) > finAt);
  check('there is a glow behind it', ops.some((o) => o.op === 'fillRect' && o.depth === 0));

  // A save without its restore leaks the whale's rotation onto every
  // layer after it, and the water would visibly tilt.
  check('every save is restored', sea.canvas.ctx.depth === 0, String(sea.canvas.ctx.depth));
  // Not ops[0] – the constructor's setTransform lands before any frame.
  // What matters is that nothing is painted onto the previous frame.
  const paints = ['fill', 'fillPath', 'fillRect', 'stroke'];
  check('nothing is painted before the frame is cleared',
    ops.findIndex((o) => o.op === 'clearRect') <
    ops.findIndex((o) => paints.indexOf(o.op) >= 0));
}

// --- a band with no whale in it ----------------------------------------

console.log('\nwhale off');

{
  const world = makeWorld();
  world.ocean({ 'data-whale': 'off' });
  const W = world.load();
  const sea = W.mounted[0];
  sea.canvas.ctx.ops.length = 0;
  world.frame(16);
  const ops = sea.canvas.ctx.ops;
  check('no whale is drawn', !ops.some((o) => o.op === 'fillPath'));
  check('and no glow either', !ops.some((o) => o.op === 'fillRect'));
  check('but the water still is',
    ops.filter((o) => o.op === 'fill' && o.depth === 0).length === W.LAYERS.length);
}

// --- a band with a whale in it ------------------------------------------

console.log('\nband');

{
  const world = makeWorld();
  world.ocean({ 'data-ocean': 'band' });
  const W = world.load();
  const sea = W.mounted[0];
  check('a band mounts as a band', sea.layout === 'band');
  check('with the band layer table', sea.layers === W.BAND_LAYERS);
  check('and the emblem-sized whale span',
    sea.st.whaleSpan === W.PROFILES.band.span, String(sea.st.whaleSpan));
  sea.canvas.ctx.ops.length = 0;
  world.frame(16);
  const ops = sea.canvas.ctx.ops;
  check('the band still draws its whale',
    ops.some((o) => o.op === 'fillPath' && o.args[0] === W.WHALE.body));
  check('and its water', ops.filter((o) => o.op === 'fill' && o.depth === 0).length ===
    W.BAND_LAYERS.length);
  sea.destroy();
}

// --- the night sky ------------------------------------------------------

console.log('\nstars');

{
  // The dark theme resolves the sky tokens to real colours.
  const world = makeWorld({
    palette: {
      '--sky-star': 'rgb(214, 230, 255)',
      '--sky-star-warm': 'rgb(255, 229, 184)',
    },
  });
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  sea.canvas.ctx.ops.length = 0;
  world.frame(16);
  const ops = sea.canvas.ctx.ops;

  // At 16ms there is no spout yet and the eye is inside the whale's
  // transform, so every depth-0 arc in the frame is a star.
  const starArcs = ops.filter((o) => o.op === 'arc' && o.depth === 0);
  const field = W.starField(sea.w, sea.h);
  check('a dark sky is full of stars', starArcs.length === field.length,
    `${starArcs.length} vs ${field.length}`);
  check('every star stays above the water',
    starArcs.every((o) => o.args[1] < sea.h * W.SKY));

  // Every star is one fill; the water is one fill per layer. If these
  // stop adding up, something is painting that should not be.
  const fills = ops.filter((o) => o.op === 'fill' && o.depth === 0);
  check('every star is painted, and the water still is',
    fills.length === field.length + W.LAYERS.length,
    String(fills.length));

  // Foam lines and glints are the only strokes in a frame, and the
  // glints belong to exactly the stars the power law made bright.
  const bright = field.filter((s) => s.r > 1.7).length;
  const foamy = W.LAYERS.filter((l) => l.foam > 0).length;
  const strokes = ops.filter((o) => o.op === 'stroke' && o.depth === 0).length;
  check('only the bright stars glint', strokes === foamy + bright,
    `${strokes} strokes for ${bright} bright stars + ${foamy} foam lines`);
  check('and some are bright enough to', bright > 0, String(bright));

  // Stars are scenery behind everything: all of them land before the
  // whale is drawn.
  const bodyAt = ops.findIndex((o) => o.op === 'fillPath');
  const lastStar = ops.lastIndexOf(ops.filter((o) => o.op === 'arc' && o.depth === 0).pop());
  check('the sky is behind the whale', bodyAt > lastStar);
  sea.destroy();
}

{
  // The light theme resolves them to transparent – the fallback the test
  // world hands back by default – and the pass is skipped whole.
  const world = makeWorld();
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  sea.canvas.ctx.ops.length = 0;
  world.frame(16);
  check('daylight has no stars',
    !sea.canvas.ctx.ops.some((o) => o.op === 'arc' && o.depth === 0));
  sea.destroy();
}

// --- when it must stop --------------------------------------------------

console.log('\nstopping');

{
  const world = makeWorld({ reducedMotion: true });
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  check('reduced motion never asks for a frame', world.rafs.length === 0);
  check('but still paints one, so the page is not simply missing a sea',
    sea.frames === 1, String(sea.frames));
  check('and reports itself stopped', sea.running() === false);
}

{
  const world = makeWorld();
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  check('an on-screen ocean animates', sea.running() === true);

  world.doc.hidden = true;
  world.doc.fire('visibilitychange');
  check('a hidden tab stops it', sea.running() === false);
  world.doc.hidden = false;
  world.doc.fire('visibilitychange');
  check('and showing the tab starts it again', sea.running() === true);

  world.observers.intersect.forEach((fn) => fn([{ isIntersecting: false }]));
  check('scrolling it off-screen stops it', sea.running() === false);
  world.observers.intersect.forEach((fn) => fn([{ isIntersecting: true }]));
  check('scrolling it back starts it', sea.running() === true);

  const before = sea.frames;
  sea.destroy();
  world.frame(32);
  check('destroy stops it', sea.running() === false);
  check('and it draws nothing more', sea.frames === before);
  check('and takes its canvas with it', world.doc.oceans[0].children.length === 0);
  check('and drops its window listeners',
    (world.listeners.window.pointermove || []).length === 0);
}

// --- interaction --------------------------------------------------------

console.log('\ninteraction');

{
  const world = makeWorld();
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  world.frame(16);

  world.fireWindow('pointermove', { clientX: 300, clientY: 200 });
  check('the pointer aims the swell', sea.st.bulge.x === 300);
  check('and asks for it to rise', sea.st.bulge.target === 1);
  world.fireWindow('blur');
  check('leaving the page lets it settle', sea.st.bulge.target === 0);

  world.fireWindow('pointerdown', { clientX: 720, clientY: 240 });
  check('a click makes a ripple', sea.st.ripples.length === 1);
  check('where it was clicked', sea.st.ripples[0].x === 720);

  // Held down, a ripple per frame would pile up without bound.
  for (let i = 0; i < 60; i++) world.fireWindow('pointerdown', { clientX: 100 + i, clientY: 10 });
  check('holding the button down does not pile them up',
    sea.st.ripples.length <= 13, String(sea.st.ripples.length));

  // Clicking near the whale brings its next spout forward.
  const sea2 = W.mount(world.ocean({}), {});
  sea2.t = 5;
  sea2.st.nextSpout = 100;
  sea2.splash(sea2.w * sea2.st.whaleX, 1);
  check('a click near the whale startles it into blowing', sea2.st.nextSpout < 6);
  sea2.advance(0.3);
  check('and it does blow', sea2.st.spout.length > 0, String(sea2.st.spout.length));
  const high = sea2.st.spout.every((p) => p.vy < 0);
  check('upward', high);
  for (let i = 0; i < 200; i++) sea2.advance(0.05);
  check('and the plume falls back and clears', sea2.st.spout.length === 0);
  sea2.destroy();
}

{
  const world = makeWorld();
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  world.sandbox.pageYOffset = 0;
  world.fireWindow('scroll');
  world.sandbox.pageYOffset = 900;
  world.fireWindow('scroll');
  sea.advance(0.05);
  check('scrolling roughs the water up', sea.st.chop > 0, String(sea.st.chop));
  for (let i = 0; i < 200; i++) sea.advance(0.05);
  check('and it settles again', sea.st.chop < 0.01, String(sea.st.chop));
  sea.destroy();
}

// --- the descent --------------------------------------------------------

// Scroll *position*, as opposed to the scroll *speed* the block above
// covers. The two are separate signals into the same state and the easy
// mistake is wiring one to the other's job, so they are tested apart.

console.log('\ngoing deep');

{
  const world = makeWorld({ pageHeight: 5000, innerHeight: 1000 });
  world.ocean({});
  world.gauge();
  const W = world.load();
  const sea = W.mounted[0];

  check('a page at the top is at the surface', W.scroll.depth === 0, String(W.scroll.depth));
  world.scrollTo(0.5);
  check('halfway down the page is halfway down the water',
    Math.abs(W.scroll.depth - 0.5) < 0.001, String(W.scroll.depth));
  check('and it is published to CSS for the page furniture',
    parseFloat(world.doc.documentElement.style['--depth']) === 0.5,
    String(world.doc.documentElement.style['--depth']));
  check('and written into the gauge',
    world.doc.gauges[0].children[0].textContent === '-500 m',
    world.doc.gauges[0].children[0].textContent);
  world.scrollTo(0);
  check('the surface reads zero, not minus zero',
    world.doc.gauges[0].children[0].textContent === '0 m',
    world.doc.gauges[0].children[0].textContent);
  world.scrollTo(0.5);

  // The sea chases the published depth rather than snapping to it.
  check('the water does not teleport to it', sea.st.depth < 0.5, String(sea.st.depth));
  for (let i = 0; i < 200; i++) sea.advance(0.05);
  check('but it does get there', Math.abs(sea.st.depth - 0.5) < 0.01, String(sea.st.depth));

  // Every layer has to clear the top of the frame by the bottom of the
  // page, or the deep is just the sea sitting slightly higher up.
  const atBottom = { depth: 1, chop: 0, ripples: [], bulge: { strength: 0 } };
  const cleared = W.LAYERS.every((l) => W.layerBase(l, atBottom) < 0);
  check('at the bottom of the page the surface is overhead', cleared);
  check('and nearer layers climbed further than far ones',
    W.layerBase(W.LAYERS[3], atBottom) < W.layerBase(W.LAYERS[0], atBottom) + 0.31,
    String(W.layerBase(W.LAYERS[0], atBottom) - W.layerBase(W.LAYERS[3], atBottom)));
  check('a state with no depth in it is at the surface',
    W.layerBase(W.LAYERS[0], { depth: 0 }) === W.layerBase(W.LAYERS[0], {}));

  // The whale lags the surface. Without that it leaves with the light and
  // the deep is an empty canvas.
  const surface = W.layerBase(W.LAYERS[W.WHALE_LAYER], atBottom);
  const whale = W.whaleTransform(0, 1200, 400, {
    depth: 1, chop: 0, ripples: [], bulge: { strength: 0 },
    whaleX: 0.5, whaleSpan: 1, facing: 1,
  });
  check('the whale sinks with you but stays in frame',
    whale.y > surface * 400 && whale.y > 0 && whale.y < 400,
    `${Math.round(whale.y)} vs surface ${Math.round(surface * 400)}`);

  sea.destroy();
}

{
  // A page no longer than its window cannot be descended, and the
  // arithmetic for that is a division by zero.
  const world = makeWorld({ pageHeight: 800, innerHeight: 800 });
  world.ocean({});
  const W = world.load();
  world.scrollTo(1);
  check('a page with nothing to scroll is never deep', W.scroll.depth === 0, String(W.scroll.depth));
  W.mounted[0].destroy();
}

// --- seek ---------------------------------------------------------------

console.log('\nseek');

{
  const world = makeWorld();
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  const fire = (x, y) => world.fireWindow('pointerdown', { clientX: x, clientY: y });

  fire(200, 100);
  check('a click sends a ping', sea.st.pings.length === 1);
  check('from where it was clicked',
    sea.st.pings[0].x === 200 && sea.st.pings[0].y === 100);
  check('and the ring starts at nothing and grows',
    W.pingRadius(sea.st.pings[0], sea.t, sea.w) === 0);
  sea.advance(0.5);
  check('outward', W.pingRadius(sea.st.pings[0], sea.t, sea.w) > 0);

  for (let i = 0; i < 20; i++) fire(200, 100);
  check('a held button does not queue them up without limit',
    sea.st.pings.length <= 7, String(sea.st.pings.length));

  for (let i = 0; i < 100; i++) sea.advance(0.05);
  check('and a spent ping is forgotten', sea.st.pings.length === 0);
  sea.destroy();
}

{
  // The return. A ping aimed at where the whale is has to light it up
  // when the ring gets there – and not before, which is the half that
  // makes it read as an echo rather than a highlight.
  const world = makeWorld();
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  world.frame(16);
  const pos = sea.whalePos;
  check('the whale reports where it was drawn', !!pos && pos.x > 0);

  world.fireWindow('pointerdown', { clientX: pos.x + 400, clientY: pos.y });
  sea.advance(0.05);
  check('nothing comes back straight away', sea.st.echo === 0, String(sea.st.echo));
  let peak = 0;
  for (let i = 0; i < 40; i++) { sea.advance(0.05); peak = Math.max(peak, sea.st.echo); }
  check('the ring reaches it and it answers', peak > 0.3, String(peak));
  for (let i = 0; i < 60; i++) sea.advance(0.05);
  check('and the answer fades', sea.st.echo < 0.02, String(sea.st.echo));
  sea.destroy();
}

{
  // The DOM half of the same gesture.
  const world = makeWorld();
  world.ocean({});
  const W = world.load();
  const btn = makeEl('a', world.doc);
  btn.className = 'btn';
  world.doc.fire('pointerdown', { target: btn });
  check('clicking a control pings the control too', btn.classList.contains('is-seeking'));
  world.doc.fire('animationend', { animationName: 'seek-out', target: btn });
  check('and it is cleaned up when the pulse ends', !btn.classList.contains('is-seeking'));
  W.mounted[0].destroy();
}

{
  const world = makeWorld({ reducedMotion: true });
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  const btn = makeEl('a', world.doc);
  btn.className = 'btn';
  world.doc.fire('pointerdown', { target: btn });
  check('reduced motion gets no pulse at all', !btn.classList.contains('is-seeking'));
  world.fireWindow('pointerdown', { clientX: 100, clientY: 100 });
  check('and no ring frozen on the canvas either', sea.st.pings.length === 0);
  check('though the water still answers the click',
    sea.st.ripples.length === 1, String(sea.st.ripples.length));
  sea.destroy();
}

// --- sizing and theme ---------------------------------------------------

console.log('\nsizing and theme');

{
  const world = makeWorld({ dpr: 4 });
  const host = world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  check('the canvas is sized in CSS pixels', sea.canvas.style.width === '1200px');
  check('and backed at the device ratio, capped at 2',
    sea.canvas.width === 2400, String(sea.canvas.width));

  host.box = { width: 600, height: 200, left: 0, top: 0, right: 600, bottom: 200 };
  world.observers.resize.forEach((fn) => fn());
  check('a resize is picked up', sea.w === 600 && sea.canvas.width === 1200,
    `${sea.w} ${sea.canvas.width}`);
  check('and the sample buffer is resized with it',
    sea.pts.length === sea.n && sea.n === Math.ceil(600 / sea.step) + 2);
  sea.destroy();
}

{
  const world = makeWorld({ palette: { '--sea-0': '#abcdef', '--whale': '#123456' } });
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  check('the palette comes from CSS', sea.palette.sea[0] === '#abcdef');
  check('including the whale', sea.palette.whale === '#123456');
  check('and unset tokens fall back rather than painting undefined',
    typeof sea.palette.foam === 'string' && sea.palette.foam.length > 0);

  world.sandbox.getComputedStyle = () => ({
    getPropertyValue: () => '', color: 'rgb(255, 0, 0)',
  });
  world.observers.mutate.forEach((fn) => fn());
  check('the theme toggle repaints it', sea.palette.sea[0] === 'rgb(255, 0, 0)',
    sea.palette.sea[0]);
  sea.destroy();
}

// --- the bug that shipped ----------------------------------------------

console.log('\nresolving colours');

{
  // Every token on this site is declared as light-dark(). Reading the
  // custom property gives that back verbatim; only a real property
  // resolves it. Painting with the raw text throws on the first
  // addColorStop and the whole sea silently becomes a blank hole.
  const light = 'light-dark(#bfe4ef, #0a4d63)';
  const world = makeWorld({
    raw: { '--sea-0': light, '--sea-1': light, '--sea-2': light, '--sea-3': light },
    palette: {
      '--sea-0': 'rgb(10, 77, 99)', '--sea-1': 'rgb(10, 100, 128)',
      '--sea-2': 'rgb(6, 127, 158)', '--sea-3': 'rgb(0, 162, 196)',
      '--whale': 'rgb(18, 63, 92)',
    },
  });
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];

  check('no colour reaches the canvas unresolved',
    !JSON.stringify(sea.palette).includes('light-dark'), JSON.stringify(sea.palette));
  check('the resolved theme colour is what gets painted',
    sea.palette.sea[0] === 'rgb(10, 77, 99)', sea.palette.sea[0]);
  check('and it painted, rather than throwing on the first gradient',
    sea.frames === 1, String(sea.frames));

  // Belt and braces: the shim rejects what a real canvas rejects, so a
  // regression would surface as a thrown frame rather than a silent one.
  let threw = null;
  try {
    sea.palette.sea[0] = light;
    sea.draw(1);
  } catch (e) { threw = e.message; }
  check('painting an unresolved token is a loud failure, not a quiet one',
    threw && threw.includes('could not be parsed as a color'), String(threw));
}

// --- an old browser -----------------------------------------------------

console.log('\ndegrading');

{
  const world = makeWorld({ noObservers: true });
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  check('no ResizeObserver still leaves a working sea', sea && sea.frames >= 0);
  world.frame(16);
  check('and it draws', sea.canvas.ctx.ops.length > 0);
  // Two of them, and they are not a double-wire: one re-measures the
  // canvas, the other re-measures the scrollable range, because a window
  // that changed shape puts the same scroll offset at a different depth.
  // Counting is still how a genuine double-wire would be caught.
  check('falling back to a window resize listener',
    (world.listeners.window.resize || []).length === 2,
    String((world.listeners.window.resize || []).length));
  sea.destroy();
}

{
  const world = makeWorld();
  delete world.sandbox.Path2D;
  world.ocean({});
  const W = world.load();
  const sea = W.mounted[0];
  sea.canvas.ctx.ops.length = 0;
  world.frame(16);
  check('no Path2D means no whale, not a broken frame',
    sea.canvas.ctx.ops.filter((o) => o.op === 'fill' && o.depth === 0).length === W.LAYERS.length);
  sea.destroy();
}

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('\nthe sea runs');
