// The "right now" strip on the pricing page.
//
// The page embeds its schedule as JSON: one row per billing period, with
// a daily window in minutes of the UTC day (upstream defines the
// boundaries in UTC), a multiplier on that era's base card, and the
// instant the row takes effect. This script only reads that table; it
// holds no prices and no dates of its own, so the strip cannot disagree
// with the schedule the reader sees, and pricing.test.js can pin the
// arithmetic against the same rows the page ships.
//
// The semantics mirror internal/deepseek/pricing.go: among rows sharing
// the latest effective instant <= now (the current era), a windowed row
// wins inside its [start, end) window, and the era's windowless row is
// the answer everywhere else. Window starts are inclusive, ends
// exclusive, so 04:00:00 UTC is already off-peak.
(function (global) {
  'use strict';

  function pad2(n) {
    return (n < 10 ? '0' : '') + n;
  }

  // Minute of the UTC day, the unit the schedule's windows are in.
  function utcMinute(date) {
    return date.getUTCHours() * 60 + date.getUTCMinutes();
  }

  // The distinct effective instants, ascending, in ms. Each is an era.
  function eras(schedule) {
    var seen = {};
    var out = [];
    for (var i = 0; i < schedule.length; i++) {
      var t = Date.parse(schedule[i].effective);
      if (!seen[t]) {
        seen[t] = true;
        out.push(t);
      }
    }
    out.sort(function (a, b) { return a - b; });
    return out;
  }

  // The era in force: the latest effective instant <= now. Before the
  // first era (which cannot happen on the live page, its flat row
  // predates the page) the first era answers rather than nothing.
  function eraAt(schedule, date) {
    var all = eras(schedule);
    var t = date.getTime();
    var cur = all[0];
    for (var i = 0; i < all.length; i++) {
      if (all[i] <= t) cur = all[i];
    }
    return cur;
  }

  // The schedule row that bills this instant.
  function periodFor(schedule, date) {
    var era = eraAt(schedule, date);
    var m = utcMinute(date);
    var base = null;
    for (var i = 0; i < schedule.length; i++) {
      var r = schedule[i];
      if (Date.parse(r.effective) !== era) continue;
      if (r.start === null) {
        base = r;
        continue;
      }
      if (m >= r.start && m < r.end) return r;
    }
    return base;
  }

  // The next instant the answer above changes: the next era's effective
  // instant, or the next window boundary of the current era, whichever
  // comes first. Null only for a one-row schedule with no windows.
  function nextChange(schedule, date) {
    var t = date.getTime();
    var all = eras(schedule);
    var era = eraAt(schedule, date);
    var candidates = [];
    for (var i = 0; i < all.length; i++) {
      if (all[i] > t) {
        candidates.push(all[i]);
        break;
      }
    }
    var bounds = [];
    for (var j = 0; j < schedule.length; j++) {
      var r = schedule[j];
      if (Date.parse(r.effective) !== era || r.start === null) continue;
      bounds.push(r.start, r.end);
    }
    if (bounds.length) {
      var midnight = Date.UTC(
        date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate());
      for (var d = 0; d <= 1; d++) {
        for (var k = 0; k < bounds.length; k++) {
          var at = midnight + d * 86400000 + bounds[k] * 60000;
          if (at > t) candidates.push(at);
        }
      }
    }
    if (!candidates.length) return null;
    return new Date(Math.min.apply(null, candidates));
  }

  // A wait, the way a person reads one: days and hours far out, minutes
  // when it is close. Matches humanUntil in the CLI.
  function humanUntil(ms) {
    if (ms < 0) ms = 0;
    var mins = Math.floor(ms / 60000);
    var hours = Math.floor(mins / 60);
    if (hours >= 48) return 'in ' + Math.floor(hours / 24) + 'd' + (hours % 24) + 'h';
    if (hours >= 1) return 'in ' + hours + 'h' + pad2(mins % 60) + 'm';
    return 'in ' + mins + 'm';
  }

  function hm(h, m) {
    return pad2(h) + ':' + pad2(m);
  }

  function localHM(date) {
    var off = -date.getTimezoneOffset();
    var sign = off < 0 ? '-' : '+';
    var abs = Math.abs(off);
    return hm(date.getHours(), date.getMinutes()) +
      ' (UTC' + sign + hm(Math.floor(abs / 60), abs % 60) + ')';
  }

  function utcHM(date) {
    return hm(date.getUTCHours(), date.getUTCMinutes());
  }

  // Beijing is fixed arithmetic, not a timezone database: UTC+8, no
  // daylight saving since 1991. Same reasoning as the CLI.
  function beijingHM(date) {
    var m = (utcMinute(date) + 8 * 60) % 1440;
    return hm(Math.floor(m / 60), m % 60);
  }

  function utcStamp(date) {
    return date.getUTCFullYear() + '-' +
      pad2(date.getUTCMonth() + 1) + '-' + pad2(date.getUTCDate()) + ' ' +
      utcHM(date) + ' UTC';
  }

  // The strip's one sentence set: the period, the visitor's clocks, and
  // when the answer changes.
  function clockLine(schedule, date) {
    var p = periodFor(schedule, date);
    var parts = [];
    if (p.multiplier !== 1) {
      parts.push('Peak right now: calls bill at ' + p.multiplier +
        'x the off-peak card.');
    } else if (p.label === 'flat') {
      parts.push('The flat card applies right now, as at every hour before the switch.');
    } else {
      parts.push('Off-peak right now: calls bill at the off-peak card.');
    }
    parts.push('Your clock ' + localHM(date) + ' \u00b7 ' + utcHM(date) +
      ' UTC \u00b7 ' + beijingHM(date) + ' Beijing.');
    var next = nextChange(schedule, date);
    if (next) {
      var wait = humanUntil(next.getTime() - date.getTime());
      if (eraAt(schedule, next) !== eraAt(schedule, date)) {
        parts.push('Peak/off-peak billing on the new card begins ' + wait +
          ', at ' + utcStamp(next) + '.');
      } else {
        var np = periodFor(schedule, next);
        var verb = np.multiplier > p.multiplier ? 'Peak begins ' : 'Off-peak returns ';
        parts.push(verb + wait + ', at ' + utcHM(next) + ' UTC.');
      }
    }
    return parts.join(' ');
  }

  // Swap the committed no-JavaScript verdict for the live answer, and
  // return the updater so a timer can keep it honest across a boundary.
  function wire(doc) {
    var tag = doc.getElementById('price-schedule');
    var line = doc.getElementById('price-now-line');
    if (!tag || !line) return null;
    var schedule = JSON.parse(tag.textContent);
    var update = function () {
      line.textContent = clockLine(schedule, new Date());
    };
    update();
    return update;
  }

  var api = {
    utcMinute: utcMinute,
    eras: eras,
    eraAt: eraAt,
    periodFor: periodFor,
    nextChange: nextChange,
    humanUntil: humanUntil,
    localHM: localHM,
    utcHM: utcHM,
    beijingHM: beijingHM,
    clockLine: clockLine,
    wire: wire,
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  global.DSPricing = api;

  if (typeof document !== 'undefined' && document.getElementById) {
    var start = function () {
      var update = wire(document);
      // A reader who leaves the tab open crosses period boundaries;
      // refresh once a minute so the strip never names the wrong one.
      if (update) setInterval(update, 60000);
    };
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', start);
    } else {
      start();
    }
  }
})(typeof self !== 'undefined' ? self : this);
