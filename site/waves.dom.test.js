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
// (drawn inside a transform) from the water (drawn outside one) — and to
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
// "light-dark(#bfe4ef, #0a4d63)" — which is what getComputedStyle hands
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
    classList: { added: [], add(c) { this.added.push(c); } },
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
    createElement(tag) { return makeEl(tag, doc); },
    querySelectorAll() { return doc.oceans; },
    addEventListener(k, fn) { (listeners.document[k] = listeners.document[k] || []).push(fn); },
    removeEventListener(k, fn) {
      listeners.document[k] = (listeners.document[k] || []).filter((f) => f !== fn);
    },
    fire(k, ev) { (listeners.document[k] || []).forEach((fn) => fn(ev || {})); },
  };
  doc.documentElement = makeEl('html', doc);
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
  // Not ops[0] — the constructor's setTransform lands before any frame.
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
  // The light theme resolves them to transparent — the fallback the test
  // world hands back by default — and the pass is skipped whole.
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
  check('falling back to a window resize listener',
    (world.listeners.window.resize || []).length === 1);
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
