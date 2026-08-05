// The wave field, the whale, and the two contracts around them.
//
// None of this needs a browser: the height field is arithmetic and the
// whale's position is read out of it, so both can be checked directly.
// What cannot be checked by looking at the page is the part that fails
// silently — a renamed CSS token falls back to a hard-coded default and
// the sea just stops following the theme, and a whale edited in waves.js
// but not in whale.svg is only visible to readers with JavaScript off.
// Both are pinned here.
//
//   node site/waves.test.js

const fs = require('fs');
const path = require('path');

const W = require('./waves.js');

let failures = 0;

function check(name, ok, detail) {
  if (ok) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ': ' + detail : ''}`);
    failures++;
  }
}

function near(a, b, eps) {
  return Math.abs(a - b) <= (eps === undefined ? 1e-9 : eps);
}

function quiet(over) {
  return Object.assign({
    chop: 0,
    ripples: [],
    spout: [],
    bulge: { x: 0, strength: 0, target: 0 },
    whaleX: 0.5,
    whaleSpan: 0.6,
    facing: 1,
    nextSpout: 1e9,
  }, over || {});
}

// --- the layer table ----------------------------------------------------

console.log('layers');

const L = W.LAYERS;
check('four layers', L.length === 4, String(L.length));
check('each layer rests below the one behind it',
  L.every((l, i) => i === 0 || l.base > L[i - 1].base),
  L.map((l) => l.base).join(' '));
check('nearer layers pick up more of the interaction',
  L.every((l, i) => i === 0 || l.reach > L[i - 1].reach),
  L.map((l) => l.reach).join(' '));
check('nearer layers are more opaque',
  L.every((l, i) => i === 0 || l.alpha > L[i - 1].alpha));
check('every alpha is a usable fraction',
  L.every((l) => l.alpha > 0 && l.alpha <= 1));
check('every layer has a distinct tint slot',
  new Set(L.map((l) => l.tint)).size === L.length);
check('the whale has water in front of it and behind it',
  W.WHALE_LAYER > 0 && W.WHALE_LAYER < L.length, String(W.WHALE_LAYER));
// Adjacent layers moving the same way march in step and the parallax dies.
check('layers drift against each other',
  L.every((l, i) => i === 0 || Math.sign(l.speed) !== Math.sign(L[i - 1].speed)),
  L.map((l) => l.speed).join(' '));

// --- the height field ---------------------------------------------------

console.log('\nwaves');

const w = 1200;
const h = 400;
const lay = L[W.WHALE_LAYER];

// The three sines sum to at most 1 + 0.42 + 0.23 of the layer amplitude.
let peak = 0;
for (let t = 0; t < 40; t += 0.13) {
  for (let x = 0; x <= w; x += 7) peak = Math.max(peak, Math.abs(W.waveAt(lay, x, t, w)));
}
check('the surface stays inside the amplitude it declares',
  peak <= lay.amp * 1.65 + 1e-12, `${peak} > ${lay.amp * 1.65}`);
check('and gets most of the way there', peak > lay.amp * 1.2, String(peak));

check('the surface moves',
  W.waveAt(lay, 600, 0, w) !== W.waveAt(lay, 600, 1.5, w));

// A sum of sines with rationally related frequencies repeats inside one
// screen; these do not, which is why the ratios are 2.17 and 3.71.
let sameAsStart = 0;
for (let x = 1; x <= w; x += 1) {
  if (near(W.waveAt(lay, x, 0, w), W.waveAt(lay, 0, 0, w), 1e-6)) sameAsStart++;
}
check('the surface does not visibly repeat across one width',
  sameAsStart <= 2, `${sameAsStart} exact repeats of x=0`);

// --- clicks -------------------------------------------------------------

console.log('\nripples');

const r = { x: 500, t0: 10, amp: 20 };
check('a click does nothing before it happens', W.rippleAt(r, 500, 9.5, w) === 0);
check('and nothing once it has died',
  W.rippleAt(r, 500, 10 + W.RIPPLE_LIFE + 0.01, w) === 0);
check('a ripple is symmetric about where it was struck',
  near(W.rippleAt(r, 500 - 130, 10.6, w), W.rippleAt(r, 500 + 130, 10.6, w), 1e-12));

// The packet's envelope peaks where |x - x0| equals speed * age, so the
// energy is further out at every later sample.
function crest(t) {
  let best = 0;
  let at = 0;
  for (let x = 500; x <= w; x += 2) {
    const v = Math.abs(W.rippleAt(r, x, t, w));
    if (v > best) { best = v; at = x; }
  }
  return { at, v: best };
}
const c1 = crest(10.3);
const c2 = crest(10.9);
check('the ripple travels outward', c2.at > c1.at + 50, `${c1.at} -> ${c2.at}`);
check('and loses height as it goes', c2.v < c1.v, `${c1.v} -> ${c2.v}`);

// --- the pointer --------------------------------------------------------

console.log('\npointer');

check('an idle pointer leaves the water flat',
  W.bulgeAt({ x: 600, strength: 0 }, 600, w, h) === 0);
const pull = W.bulgeAt({ x: 600, strength: 1 }, 600, w, h);
check('the water is pulled up toward the pointer, not pushed down',
  pull < 0, String(pull));
check('and the pull falls away with distance',
  Math.abs(W.bulgeAt({ x: 600, strength: 1 }, 900, w, h)) < Math.abs(pull) * 0.2);

const st = quiet();
const flat = W.surfaceAt(lay, 600, 3, w, h, st);
const stirred = W.surfaceAt(lay, 600, 3, w, h, quiet({ bulge: { x: 600, strength: 1 } }));
check('the pointer moves the surface it is over', stirred < flat - 1);

// Chop scales the wave term, so a scrolling reader gets rougher water.
const calm = W.surfaceAt(lay, 313, 3, w, h, quiet({ chop: 0 }));
const rough = W.surfaceAt(lay, 313, 3, w, h, quiet({ chop: 1 }));
check('scroll energy roughens the surface', Math.abs(rough - lay.base * h) >
  Math.abs(calm - lay.base * h), `${calm} vs ${rough}`);

// --- the whale ----------------------------------------------------------

console.log('\nwhale');

const tr = W.whaleTransform(4.2, w, h, st);
check('the whale is scaled to the span it was asked for',
  near(tr.span, w * st.whaleSpan, 1e-9), `${tr.span}`);
check('the box maps onto that span', near(tr.sx * W.WHALE.view.w, tr.span, 1e-9));
check('a very wide one does not get an absurd one',
  W.whaleTransform(0, 4000, h, st).span === 1200);
check('facing right mirrors it', W.whaleTransform(4.2, w, h, quiet({ facing: -1 })).sx < 0);

// On a phone the same fraction of a much smaller width is a much smaller
// whale, so it is widened and centred. Centred because at that width it
// nearly spans the screen and an off-centre one loses its fluke.
const phone = W.whaleTransform(0, 380, h, st);
check('a phone gets a proportionally bigger whale',
  phone.span / 380 > st.whaleSpan + 0.3, String(phone.span / 380));
check('and it still fits across the screen', phone.span < 380);
check('and is centred rather than offset', Math.abs(phone.x - 190) < 380 * 0.02,
  String(phone.x));
for (let ww = 320; ww <= 1600; ww += 20) {
  const g = W.whaleTransform(0, ww, h, quiet());
  if (g.span > ww) {
    check('the whale never grows wider than the viewport', false, `${ww} -> ${g.span}`);
    break;
  }
  if (ww === 1600) check('the whale never grows wider than the viewport', true);
}

// The tilt is the slope of the water under it — this is the whole reason
// it looks like it is floating rather than being animated on a timer.
let tiltedBothWays = 0;
let worst = 0;
for (let t = 0; t < 30; t += 0.37) {
  const g = W.whaleTransform(t, w, h, st);
  const d = g.span * W.SLOPE_SPAN;
  const slope = (W.surfaceAt(lay, g.x + d, t, w, h, st) -
                 W.surfaceAt(lay, g.x - d, t, w, h, st)) / (2 * d);
  const want = Math.max(-W.TILT_MAX,
    Math.min(W.TILT_MAX, Math.atan(slope) * W.TILT_GAIN));
  if (!near(g.rot, want, 1e-9)) {
    check('the whale leans with the water under it', false, `t=${t}`);
    tiltedBothWays = -999;
    break;
  }
  worst = Math.max(worst, Math.abs(g.rot));
  if (g.rot > 0.004) tiltedBothWays |= 1;
  if (g.rot < -0.004) tiltedBothWays |= 2;
}
if (tiltedBothWays >= 0) check('the whale leans with the water under it', true);
check('and leans both ways as the swell passes', tiltedBothWays === 3,
  String(tiltedBothWays));

// It bridges several wavelengths, so it should roll gently rather than
// pitch. Measuring the slope locally instead of across its own length
// had it swinging through 25 degrees on a phone — a shipwreck.
check('the tilt ceiling is a roll, not a capsize',
  W.TILT_MAX < 0.14, `${(W.TILT_MAX * 180 / Math.PI).toFixed(1)} degrees`);
for (const width of [360, 480, 760, 1200, 1600]) {
  let peak = 0;
  let moved = 0;
  for (let t = 0; t < 40; t += 0.11) {
    const rot = W.whaleTransform(t, width, h, quiet()).rot;
    peak = Math.max(peak, Math.abs(rot));
    if (Math.abs(rot) > 0.01) moved++;
  }
  check(`at ${width}px it stays inside that ceiling`,
    peak <= W.TILT_MAX + 1e-12, `${(peak * 180 / Math.PI).toFixed(1)} degrees`);
  check(`and at ${width}px it is still visibly riding something`, moved > 20,
    `${moved} samples off level`);
}

const flatTr = { x: 100, y: 200, sx: 2, sy: 2, rot: 0, span: 100 };
const wl = W.whaleToScreen(W.WHALE.view.w / 2, W.WHALE.view.h * W.WATERLINE, flatTr);
check('the whale hangs from its waterline', near(wl.x, 100) && near(wl.y, 200),
  `${wl.x},${wl.y}`);
const blow = W.whaleToScreen(W.WHALE.blow.x, W.WHALE.blow.y, flatTr);
check('the blowhole is above the waterline and ahead of centre',
  blow.y < wl.y && blow.x < wl.x, `${blow.x},${blow.y}`);
const turned = W.whaleToScreen(W.WHALE.view.w / 2, 0, { ...flatTr, rot: Math.PI / 2 });
check('a quarter turn puts the nose-up point out to the side',
  near(turned.y, 200, 1e-6) && turned.x > 100, `${turned.x},${turned.y}`);

check('the eye sits inside the head', W.WHALE.eye.x < W.WHALE.view.w * 0.2 &&
  W.WHALE.eye.y > 150 && W.WHALE.eye.y < 282);

// --- contracts ----------------------------------------------------------

console.log('\ncontracts');

const svg = fs.readFileSync(path.join(__dirname, 'whale.svg'), 'utf8');
check('whale.svg draws the same body as the canvas does',
  svg.includes(`d="${W.WHALE.body}"`),
  'the two paths have drifted apart');
check('whale.svg draws the same flipper',
  svg.includes(`d="${W.WHALE.fin}"`));
check('whale.svg puts the eye in the same place',
  svg.includes(`cx="${W.WHALE.eye.x}" cy="${W.WHALE.eye.y}" r="${W.WHALE.eye.r}"`));
check('whale.svg declares the box the paths were drawn in',
  svg.includes(`viewBox="0 0 ${W.WHALE.view.w} ${W.WHALE.view.h}"`));

// readPalette names every custom property it wants; ask it what those
// are rather than keeping a second list here that could go stale.
const wanted = [];
W.readPalette((k) => { wanted.push(k); return ''; });
const css = fs.readFileSync(path.join(__dirname, 'style.css'), 'utf8');
const missing = wanted.filter((k) => !css.includes(k + ':'));
check(`style.css defines all ${wanted.length} colours the sea asks for`,
  missing.length === 0, missing.join(', '));

// Every one is declared twice: the dark value alone, then the
// light-dark() pair. That is the site's rule for surviving a browser
// without light-dark(), and the sea has to follow it too.
const noPair = wanted.filter((k) => !new RegExp(k + ':\\s*light-dark\\(').test(css));
check('and gives each one a light-dark() pair', noPair.length === 0, noPair.join(', '));

const fallback = W.readPalette(() => '');
const colours = fallback.sea.concat(
  Object.keys(fallback).filter((k) => k !== 'sea').map((k) => fallback[k]));
check('a missing property still yields a colour, not undefined',
  colours.every((v) => typeof v === 'string' && v.length > 0), JSON.stringify(fallback));
check('the sea is a four-colour ramp', fallback.sea.length === 4);
const live = W.readPalette((k) => (k === '--sea-0' ? '#123456' : ''));
check('a property that is set wins', live.sea[0] === '#123456');

// The pages that mount an ocean have to load the script that paints it.
for (const page of ['index.html', '404.html', 'install/index.html']) {
  const html = fs.readFileSync(path.join(__dirname, page), 'utf8');
  const mounts = (html.match(/data-ocean/g) || []).length;
  check(`${page} loads waves.js for its ${mounts} ocean(s)`,
    mounts > 0 && /<script src="[^"]*waves\.js" defer>/.test(html));
}

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('\nthe sea holds');
