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
    // The Ocean default, deliberately. A fixture that drifts from what
    // ships is a fixture that tests nothing.
    whaleSpan: 1.0,
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
// No upper clamp any more: at this size the whale is scenery that scales
// with the window, not a sprite that tops out.
check('the whale scales with the viewport rather than topping out',
  near(W.whaleTransform(0, 4000, h, st).span, 4000 * st.whaleSpan, 1e-9),
  String(W.whaleTransform(0, 4000, h, st).span));
check('facing right mirrors it', W.whaleTransform(4.2, w, h, quiet({ facing: -1 })).sx < 0);

// On a phone the same fraction of a much smaller width is a much smaller
// whale, so it is widened and centred. Centred because at that width it
// nearly spans the screen and an off-centre one loses its fluke.
const phone = W.whaleTransform(0, 380, h, st);
check('a phone gets a proportionally bigger whale',
  phone.span / 380 > st.whaleSpan + 0.3, String(phone.span / 380));
check('and it is centred', Math.abs(phone.x - 190) < 380 * 0.02, String(phone.x));

// Wider than the window at every size, on purpose: you cannot see the ends
// of an enormous animal. This is the assertion that was inverted while the
// sea was still a band, and inverting it back is the whole redesign.
for (let ww = 320; ww <= 1600; ww += 20) {
  const g = W.whaleTransform(0, ww, h, quiet());
  if (g.span < ww) {
    check('the whale is always at least as wide as the viewport', false,
      `${ww} -> ${g.span}`);
    break;
  }
  if (ww === 1600) check('the whale is always at least as wide as the viewport', true);
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
// had it swinging through 25 degrees on a phone — a shipwreck. The
// ceiling pins the other side of the contract: a ~3° slow roll, not the
// 7° see-saw against the clamp that it used to be.
check('the tilt ceiling is a roll, not a capsize',
  W.TILT_MAX < 0.08, `${(W.TILT_MAX * 180 / Math.PI).toFixed(1)} degrees`);
for (const width of [360, 480, 760, 1200, 1600]) {
  let peak = 0;
  let moved = 0;
  for (let t = 0; t < 40; t += 0.11) {
    const rot = W.whaleTransform(t, width, h, quiet()).rot;
    peak = Math.max(peak, Math.abs(rot));
    // 0.006 rad ≈ a third of a degree — the floor of what the eye can
    // catch on an animal this size. The roll should stay above it.
    if (Math.abs(rot) > 0.006) moved++;
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
check('whale.svg draws the same mouth line',
  svg.includes(`d="${W.WHALE.mouth}"`));
check('whale.svg puts the eye in the same place',
  svg.includes(`cx="${W.WHALE.eye.x}" cy="${W.WHALE.eye.y}" r="${W.WHALE.eye.r}"`));
check('whale.svg declares the box the paths were drawn in',
  svg.includes(`viewBox="0 0 ${W.WHALE.view.w} ${W.WHALE.view.h}"`));

// The favicon and the masthead both carry the mark — the sounding fluke.
// Redrawn freehand in either place it would drift the moment the mark is
// edited, and nobody reviews a favicon.
const icon = fs.readFileSync(path.join(__dirname, 'favicon.svg'), 'utf8');
check('favicon.svg carries the mark', icon.includes(`d="${W.MARK}"`));
const home = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
check('the masthead carries the same mark', home.includes(`d="${W.MARK}"`));

// readPalette names every custom property it wants; ask it what those
// are rather than keeping a second list here that could go stale.
const wanted = [];
W.readPalette((k, fb) => { wanted.push(k); return fb; });
const css = fs.readFileSync(path.join(__dirname, 'style.css'), 'utf8');
const missing = wanted.filter((k) => !css.includes(k + ':'));
check(`style.css defines all ${wanted.length} colours the sea asks for`,
  missing.length === 0, missing.join(', '));

// Every one is declared twice: the dark value alone, then the
// light-dark() pair. That is the site's rule for surviving a browser
// without light-dark(), and the sea has to follow it too.
const noPair = wanted.filter((k) => !new RegExp(k + ':\\s*light-dark\\(').test(css));
check('and gives each one a light-dark() pair', noPair.length === 0, noPair.join(', '));

const fallback = W.readPalette((_, fb) => fb);
const colours = fallback.sea.concat(
  Object.keys(fallback).filter((k) => k !== 'sea').map((k) => fallback[k]));
check('a missing property still yields a colour, not undefined',
  colours.every((v) => typeof v === 'string' && v.length > 0), JSON.stringify(fallback));
check('the sea is a four-colour ramp', fallback.sea.length === 4);
const live = W.readPalette((k, fb) => (k === '--sea-0' ? '#123456' : fb));
check('a property that is set wins', live.sea[0] === '#123456');

// The pages that mount an ocean have to load the script that paints it.
for (const page of ['index.html', '404.html', 'install/index.html']) {
  const html = fs.readFileSync(path.join(__dirname, page), 'utf8');
  const mounts = (html.match(/data-ocean/g) || []).length;
  check(`${page} loads waves.js for its ${mounts} ocean(s)`,
    mounts > 0 && /<script src="[^"]*waves\.js" defer>/.test(html));
}

// --- the band (the 404's sea under the masthead) ------------------------

console.log('\nband');

const B = W.BAND_LAYERS;
check('the band has its own four layers', B.length === 4, String(B.length));
check('and they obey the same table rules',
  B.every((l, i) => i === 0 || (l.base > B[i - 1].base && l.reach > B[i - 1].reach &&
    l.alpha > B[i - 1].alpha)) &&
  B.every((l, i) => i === 0 || Math.sign(l.speed) !== Math.sign(B[i - 1].speed)) &&
  new Set(B.map((l) => l.tint)).size === B.length);
check('the band whale has water in front of it and behind it',
  W.WHALE_LAYER > 0 && W.WHALE_LAYER < B.length);
check('the band waterline starts near the top of the band',
  B[0].base < 0.4, String(B[0].base));

// The band whale is an emblem, not scenery: it must fit inside the band
// it rides, where the viewport whale deliberately spills past the edges.
const bandSt = quiet({
  whaleSpan: W.PROFILES.band.span,
  minSpan: W.PROFILES.band.minSpan,
  narrowBoost: W.PROFILES.band.narrowBoost,
  layers: W.BAND_LAYERS,
});
const bw = W.whaleTransform(4.2, 1600, 240, bandSt);
check('the band whale stays narrower than the band', bw.span <= 1600, String(bw.span));
check('and rides the band water, not the viewport’s',
  Math.abs(bw.y - B[W.WHALE_LAYER].base * 240) < 240 * 0.1, String(bw.y));
check('the viewport whale still spans the viewport',
  W.whaleTransform(4.2, 1600, 400, quiet()).span >= 1600);

// The 404 mounts the band; style.css has to know what one is.
const notFound = fs.readFileSync(path.join(__dirname, '404.html'), 'utf8');
check('the 404 mounts its sea as a band under the masthead',
  /data-ocean="band"/.test(notFound) && !/<div class="sea" data-ocean><\/div>/.test(notFound));
check('and style.css knows what a band is', css.includes('.sea-band'));

// --- the night sky ------------------------------------------------------

console.log('\nstars');

{
  const a = W.starField(1200, 800);
  const b = W.starField(1200, 800);
  check('the same viewport deals the same sky',
    JSON.stringify(a) === JSON.stringify(b));
  check('a phone still gets a sky worth having',
    W.starField(360, 640).length >= 70);
  check('a cinema display does not get a snowstorm',
    W.starField(3840, 2160).length <= 220);

  check('every star stays out of the water',
    a.every((s) => s.y >= 0 && s.y < W.SKY),
    String(Math.max(...a.map((s) => s.y))));
  check('and on the canvas', a.every((s) => s.x >= 0 && s.x < 1));

  // The power law: many faint stars, a few bright ones — not the other
  // way round, and never uniform.
  const bright = a.filter((s) => s.r > 1.7).length;
  check('a few stars are bright enough to glint',
    bright > 0 && bright < a.length * 0.2, String(bright));
  const faint = a.filter((s) => s.r < 1).length;
  check('most stars are faint', faint > a.length / 2, String(faint));

  check('faint stars twinkle harder than bright ones', (() => {
    const sorted = [...a].sort((x, y) => x.r - y.r);
    const lo = sorted.slice(0, 20).reduce((t, s) => t + s.tw, 0) / 20;
    const hi = sorted.slice(-20).reduce((t, s) => t + s.tw, 0) / 20;
    return lo > hi;
  })());
  check('no star ever blinks out — the twinkle floor stays above zero',
    a.every((s) => s.tw < 1 && s.a * (1 - s.tw) > 0));
  check('the sky has both cool and warm stars',
    a.some((s) => s.warm) && a.some((s) => !s.warm));
}

{
  check('a transparent token means no stars',
    !W.visibleColour('rgba(0, 0, 0, 0)') && !W.visibleColour('transparent') &&
    !W.visibleColour('rgb(214 230 255 / 0)') && !W.visibleColour(''));
  check('a real colour means stars',
    W.visibleColour('rgb(214, 230, 255)') && W.visibleColour('#d6e6ff') &&
    W.visibleColour('rgba(255, 229, 184, 0.9)'));
}

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('\nthe sea holds');
