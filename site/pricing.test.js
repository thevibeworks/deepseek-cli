// The pricing page's period arithmetic, pinned to the dated schedule.
//
// Two failure modes matter here. The first is drift: the page embeds its
// schedule as JSON, build.py renders the same rows as the visible table,
// and the CLI encodes the same instants in Go - if any copy moves alone,
// the strip starts lying next to a table that tells the truth. So the
// schedule is read out of the committed HTML and compared against the
// upstream ground truth verbatim. The second is boundary arithmetic:
// window starts inclusive, ends exclusive, eras switching on their
// effective instant and not a minute before - all checked at the exact
// instants where getting it wrong is invisible until it costs money.
//
//   node site/pricing.test.js
//
// All instants below are UTC, constructed with Date.UTC, so the suite
// passes in any timezone and on any day it is run.

const fs = require('fs');
const path = require('path');

const P = require('./pricing.js');

let failures = 0;

function check(name, ok, detail) {
  if (ok) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ': ' + detail : ''}`);
    failures++;
  }
}

function at(y, mo, d, h, mi, s) {
  return new Date(Date.UTC(y, mo - 1, d, h, mi, s || 0));
}

// ---------------------------------------------------------------------
// The schedule the page actually ships, not a copy of it.

const page = fs.readFileSync(
  path.join(__dirname, 'pricing', 'index.html'), 'utf8');
const m = page.match(
  /<script id="price-schedule" type="application\/json">(.*?)<\/script>/s);
check('the page embeds its schedule as JSON', !!m);
const schedule = JSON.parse(m[1]);

// Upstream ground truth (api-docs.deepseek.com/quick_start/pricing,
// 2026-08-13): flat until 16:00 UTC 2026-08-16, then peak/off-peak with
// peak 01:00-04:00 and 06:00-10:00 UTC at twice the off-peak rate.
const truth = [
  { label: 'flat', start: null, end: null, multiplier: 1, effective: '2026-08-02T00:00:00Z' },
  { label: 'off-peak', start: null, end: null, multiplier: 1, effective: '2026-08-16T16:00:00Z' },
  { label: 'peak', start: 60, end: 240, multiplier: 2, effective: '2026-08-16T16:00:00Z' },
  { label: 'peak', start: 360, end: 600, multiplier: 2, effective: '2026-08-16T16:00:00Z' },
];
check('the embedded schedule is the upstream ground truth',
  JSON.stringify(schedule) === JSON.stringify(truth),
  JSON.stringify(schedule));

// The visible tables carry the same story: every number of both cards,
// and the no-JavaScript strip already names the date and the windows.
for (const figure of [
  '$0.0028', '$0.14', '$0.28', '$0.003625', '$0.435', '$0.87',
  '$0.007', '$0.22', '$0.66', '$0.014', '$0.44', '$1.32',
  '$0.022', '$1.98', '$0.044', '$3.96',
]) {
  check(`the page carries ${figure}`, page.includes(figure));
}
check('the no-script strip names the effective instant',
  page.includes('2026-08-16 16:00 UTC'));
check('the schedule table is windowed in UTC',
  page.includes('01:00&ndash;04:00 UTC') && page.includes('06:00&ndash;10:00 UTC'));
check('the page copy is em-dash-free', !page.includes('\u2014'));

// ---------------------------------------------------------------------
// Which period bills which instant.

function labelAt(d) {
  const p = P.periodFor(schedule, d);
  return p.label + '@' + p.multiplier;
}

check('before the flip every hour is flat, even inside a future peak window',
  labelAt(at(2026, 8, 14, 2, 30)) === 'flat@1');
check('the last minute before the flip is still flat',
  labelAt(at(2026, 8, 16, 15, 59, 59)) === 'flat@1');
check('the flip instant itself is off-peak (16:00 is outside both windows)',
  labelAt(at(2026, 8, 16, 16, 0, 0)) === 'off-peak@1');
check('01:00 UTC starts peak, inclusive',
  labelAt(at(2026, 8, 17, 1, 0, 0)) === 'peak@2');
check('inside the first window is peak',
  labelAt(at(2026, 8, 17, 2, 30)) === 'peak@2');
check('03:59 is still peak',
  labelAt(at(2026, 8, 17, 3, 59, 59)) === 'peak@2');
check('04:00 UTC ends peak, exclusive',
  labelAt(at(2026, 8, 17, 4, 0, 0)) === 'off-peak@1');
check('the gap between the windows is off-peak',
  labelAt(at(2026, 8, 17, 5, 30)) === 'off-peak@1');
check('06:00 UTC starts the second window',
  labelAt(at(2026, 8, 17, 6, 0, 0)) === 'peak@2');
check('10:00 UTC ends it',
  labelAt(at(2026, 8, 17, 10, 0, 0)) === 'off-peak@1');
check('the long overnight stretch is off-peak',
  labelAt(at(2026, 8, 17, 18, 0)) === 'off-peak@1');

// ---------------------------------------------------------------------
// When the answer changes next.

function nextAt(d) {
  return P.nextChange(schedule, d).toISOString();
}

check('before the flip the next change is the flip itself',
  nextAt(at(2026, 8, 14, 12, 0)) === '2026-08-16T16:00:00.000Z');
check('a future window boundary does not fire in the flat era',
  nextAt(at(2026, 8, 16, 0, 30)) === '2026-08-16T16:00:00.000Z');
check('at the flip instant the next change is the next peak start',
  nextAt(at(2026, 8, 16, 16, 0)) === '2026-08-17T01:00:00.000Z');
check('inside a window the next change is its end',
  nextAt(at(2026, 8, 17, 2, 30)) === '2026-08-17T04:00:00.000Z');
check('between the windows the next change is the second start',
  nextAt(at(2026, 8, 17, 4, 30)) === '2026-08-17T06:00:00.000Z');
check('after the last window the next change is tomorrow 01:00',
  nextAt(at(2026, 8, 17, 12, 0)) === '2026-08-18T01:00:00.000Z');

// ---------------------------------------------------------------------
// The strip's sentence, at instants on both sides of the flip.

const flatLine = P.clockLine(schedule, at(2026, 8, 14, 12, 0));
check('the flat-era line says the flat card applies',
  flatLine.includes('flat card applies'));
check('the flat-era line announces the new-card flip with its full date',
  flatLine.includes('2026-08-16 16:00 UTC'), flatLine);
check('the flat-era line carries the UTC clock',
  flatLine.includes('12:00 UTC'), flatLine);
check('the flat-era line carries the Beijing clock',
  flatLine.includes('20:00 Beijing'), flatLine);

const peakLine = P.clockLine(schedule, at(2026, 8, 17, 2, 30));
check('the peak line names the multiplier',
  peakLine.includes('Peak right now: calls bill at 2x the off-peak card.'), peakLine);
check('the peak line says when off-peak returns',
  peakLine.includes('Off-peak returns in 1h30m, at 04:00 UTC.'), peakLine);

const offLine = P.clockLine(schedule, at(2026, 8, 17, 23, 45));
check('the off-peak line names its period',
  offLine.includes('Off-peak right now'), offLine);
check('the off-peak line says when peak begins',
  offLine.includes('Peak begins in 1h15m, at 01:00 UTC.'), offLine);
check('no line uses an em-dash', ![flatLine, peakLine, offLine]
  .some((l) => l.includes('\u2014')));

// ---------------------------------------------------------------------
// The helpers the sentences are assembled from.

check('humanUntil far out counts days', P.humanUntil(2 * 86400000 + 5 * 3600000) === 'in 2d5h');
check('humanUntil mid-range counts hours', P.humanUntil(90 * 60000) === 'in 1h30m');
check('humanUntil close in counts minutes', P.humanUntil(14 * 60000 + 59000) === 'in 14m');
check('beijingHM is fixed +8 arithmetic',
  P.beijingHM(at(2026, 8, 17, 23, 45)) === '07:45');
check('utcMinute is the minute of the UTC day',
  P.utcMinute(at(2026, 8, 17, 6, 0)) === 360);

// ---------------------------------------------------------------------
// wire() replaces the committed verdict with the live line.

function fakeDoc(scheduleText) {
  const nodes = {
    'price-schedule': { textContent: scheduleText },
    'price-now-line': { textContent: 'committed verdict' },
  };
  return { getElementById: (id) => nodes[id] || null, nodes };
}

const doc = fakeDoc(JSON.stringify(truth));
const update = P.wire(doc);
check('wire returns an updater', typeof update === 'function');
check('wire rewrote the strip',
  doc.nodes['price-now-line'].textContent !== 'committed verdict');
check('the rewritten strip names a period',
  /flat card applies|Off-peak right now|Peak right now/
    .test(doc.nodes['price-now-line'].textContent));
check('wire on a page without the strip is a no-op',
  P.wire({ getElementById: () => null }) === null);

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('\nall ok');
