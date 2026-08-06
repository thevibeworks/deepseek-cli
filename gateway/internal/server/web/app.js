/* freeseek status dashboard — vanilla JS, no dependencies.
   Polls GET /v1/status every 5s; exponential backoff to 60s on failure. */
"use strict";
(function () {
  var $ = function (id) { return document.getElementById(id); };
  var reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

  /* ---------- formatting ---------- */

  function fmtCompact(n) {
    if (typeof n !== "number" || !isFinite(n)) return "—";
    var neg = n < 0 ? "-" : "";
    var a = Math.abs(n);
    if (a < 1000) return neg + String(Math.round(a));
    var units = [[1e9, "B"], [1e6, "M"], [1e3, "k"]];
    for (var i = 0; i < units.length; i++) {
      if (a >= units[i][0]) {
        var v = a / units[i][0];
        var s = v >= 100 ? Math.round(v).toString() : v.toFixed(1).replace(/\.0$/, "");
        return neg + s + units[i][1];
      }
    }
    return neg + String(a);
  }

  function fmtInt(n) {
    if (typeof n !== "number" || !isFinite(n)) return "—";
    return Math.round(n).toLocaleString("en-US");
  }

  function fmtDur(sec) {
    if (typeof sec !== "number" || !isFinite(sec) || sec < 0) return "—";
    sec = Math.floor(sec);
    var d = Math.floor(sec / 86400);
    var h = Math.floor((sec % 86400) / 3600);
    var m = Math.floor((sec % 3600) / 60);
    var s = sec % 60;
    if (d > 0) return d + "d " + h + "h";
    if (h > 0) return h + "h " + m + "m";
    if (m > 0) return m + "m " + s + "s";
    return s + "s";
  }

  function fmtRate(n) {
    if (typeof n !== "number" || !isFinite(n)) return "—";
    return n >= 100 ? String(Math.round(n)) : n.toFixed(1);
  }

  function setText(id, text) {
    var el = $(id);
    if (el) el.textContent = text;
  }

  /* ---------- theme ---------- */

  function currentTheme() {
    var t = document.documentElement.dataset.theme;
    if (t === "dark" || t === "light") return t;
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }

  $("theme-toggle").addEventListener("click", function () {
    var next = currentTheme() === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    try { localStorage.setItem("theme", next); } catch (e) { /* private mode */ }
    chart.refreshColors();
    chart.draw();
  });
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", function () {
    chart.refreshColors();
    chart.draw();
  });

  /* ---------- copy button ---------- */

  var copyBtn = $("copy-btn");
  copyBtn.addEventListener("click", function () {
    var text = $("install-code").innerText;
    function done(msg) {
      copyBtn.textContent = msg;
      setTimeout(function () { copyBtn.textContent = "copy"; }, 1600);
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(
        function () { done("copied"); },
        function () { done("failed"); }
      );
    } else {
      done("failed");
    }
  });

  /* ---------- state pill ---------- */

  // state -> [label, css class]. cyan = ok, yellow = busy/degraded, pink = alerts.
  var STATES = {
    operational: ["operational", "st-ok"],
    busy: ["busy", "st-busy"],
    degraded: ["degraded", "st-busy"],
    day_exhausted: ["daily budget exhausted", "st-warn"],
    credit_exhausted: ["credit pool exhausted", "st-warn"]
  };

  function setPill(label, cls) {
    var pill = $("pill");
    pill.textContent = label;
    pill.className = "pill " + cls;
  }

  /* ---------- sparkline chart ---------- */

  var chart = (function () {
    var canvas = $("spark");
    var ctx = canvas.getContext("2d");
    var target = [];      // latest series from the server
    var shown = [];       // what is currently drawn (tweens toward target)
    var tweenFrom = null;
    var tweenStart = 0;
    var raf = 0;
    var hoverIdx = -1;
    var colors = { line: "#00c2e9", grid: "#9a9a9a" };
    var TWEEN_MS = 280;

    function refreshColors() {
      var cs = getComputedStyle(canvas);
      colors.line = cs.color;
      colors.grid = cs.borderTopColor || cs.borderColor || colors.grid;
    }

    function size() {
      var dpr = window.devicePixelRatio || 1;
      var w = canvas.clientWidth || 300;
      var h = canvas.clientHeight || 150;
      if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
        canvas.width = Math.round(w * dpr);
        canvas.height = Math.round(h * dpr);
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      return { w: w, h: h };
    }

    function draw() {
      var dim = size();
      var w = dim.w, h = dim.h;
      var pad = 4;
      ctx.clearRect(0, 0, w, h);

      var s = shown;
      var n = s.length;

      // baseline
      ctx.globalAlpha = 0.5;
      ctx.strokeStyle = colors.grid;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(0, h - 0.5);
      ctx.lineTo(w, h - 0.5);
      ctx.stroke();
      ctx.globalAlpha = 1;

      if (n < 2) return;

      var max = 1;
      for (var i = 0; i < n; i++) if (s[i] > max) max = s[i];

      function x(i) { return (i / (n - 1)) * w; }
      function y(v) { return h - pad - (v / max) * (h - pad * 2); }

      // area fill
      ctx.beginPath();
      ctx.moveTo(0, h);
      for (var j = 0; j < n; j++) ctx.lineTo(x(j), y(s[j]));
      ctx.lineTo(w, h);
      ctx.closePath();
      ctx.globalAlpha = 0.16;
      ctx.fillStyle = colors.line;
      ctx.fill();
      ctx.globalAlpha = 1;

      // line
      ctx.beginPath();
      for (var k = 0; k < n; k++) {
        if (k === 0) ctx.moveTo(x(k), y(s[k]));
        else ctx.lineTo(x(k), y(s[k]));
      }
      ctx.strokeStyle = colors.line;
      ctx.lineWidth = 2;
      ctx.lineJoin = "round";
      ctx.stroke();

      // hover crosshair
      if (hoverIdx >= 0 && hoverIdx < n) {
        var hx = x(hoverIdx);
        ctx.globalAlpha = 0.6;
        ctx.strokeStyle = colors.grid;
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.moveTo(hx + 0.5, 0);
        ctx.lineTo(hx + 0.5, h);
        ctx.stroke();
        ctx.globalAlpha = 1;
        ctx.beginPath();
        ctx.arc(hx, y(s[hoverIdx]), 3.5, 0, Math.PI * 2);
        ctx.fillStyle = colors.line;
        ctx.fill();
      }
    }

    function step(ts) {
      var t = Math.min(1, (ts - tweenStart) / TWEEN_MS);
      var e = 1 - Math.pow(1 - t, 3); // ease-out cubic
      for (var i = 0; i < target.length; i++) {
        var from = tweenFrom[i] || 0;
        shown[i] = from + (target[i] - from) * e;
      }
      shown.length = target.length;
      draw();
      if (t < 1) raf = requestAnimationFrame(step);
    }

    function update(series) {
      if (!Array.isArray(series)) series = [];
      var clean = [];
      for (var i = 0; i < series.length; i++) {
        var v = Number(series[i]);
        clean.push(isFinite(v) && v > 0 ? v : 0);
      }
      var prevShown = shown.slice();
      target = clean;
      if (raf) cancelAnimationFrame(raf);
      if (reducedMotion.matches || prevShown.length === 0) {
        shown = clean.slice();
        draw();
        return;
      }
      // align previous frame to the new series length (both end at "now")
      tweenFrom = [];
      var shift = clean.length - prevShown.length;
      for (var j = 0; j < clean.length; j++) {
        var pi = j - shift;
        tweenFrom.push(pi >= 0 && pi < prevShown.length ? prevShown[pi] : 0);
      }
      tweenStart = performance.now();
      raf = requestAnimationFrame(step);
    }

    canvas.addEventListener("pointermove", function (ev) {
      var n = shown.length;
      if (n < 2) return;
      var rect = canvas.getBoundingClientRect();
      var frac = (ev.clientX - rect.left) / rect.width;
      hoverIdx = Math.max(0, Math.min(n - 1, Math.round(frac * (n - 1))));
      var age = n - 1 - hoverIdx;
      setText("spark-readout", Math.round(shown[hoverIdx]) + " tok/s · " +
        (age === 0 ? "now" : age + "s ago"));
      draw();
    });
    canvas.addEventListener("pointerleave", function () {
      hoverIdx = -1;
      setText("spark-readout", "");
      draw();
    });
    window.addEventListener("resize", draw);

    refreshColors();
    draw();
    return { update: update, draw: draw, refreshColors: refreshColors };
  })();

  /* ---------- gauges ---------- */

  function setGauge(baseId, pct) {
    var track = $(baseId);
    var fill = $(baseId + "-fill");
    var val = $(baseId + "-val");
    if (typeof pct !== "number" || !isFinite(pct)) {
      val.textContent = "—";
      return;
    }
    var p = Math.max(0, Math.min(100, pct));
    fill.style.width = p + "%";
    fill.classList.toggle("crit", p < 15);
    fill.classList.toggle("warn", p >= 15 && p < 40);
    val.textContent = p.toFixed(1) + "%";
    track.setAttribute("aria-valuenow", p.toFixed(1));
    track.setAttribute("aria-valuetext", p.toFixed(1) + "% remaining");
  }

  /* ---------- bar lists ---------- */

  function renderBars(listId, items, nameOf) {
    var ul = $(listId);
    ul.textContent = "";
    if (!Array.isArray(items) || items.length === 0) {
      var li = document.createElement("li");
      li.className = "empty";
      li.textContent = "no data yet";
      ul.appendChild(li);
      return;
    }
    var max = 1;
    items.forEach(function (it) { if (it.count > max) max = it.count; });
    items.forEach(function (it) {
      var li = document.createElement("li");
      var name = document.createElement("span");
      name.className = "name";
      name.textContent = nameOf(it);
      var bar = document.createElement("span");
      bar.className = "bar";
      bar.style.width = Math.max(2, (it.count / max) * 100) + "%";
      var count = document.createElement("span");
      count.className = "count";
      count.textContent = fmtCompact(it.count);
      li.appendChild(name);
      li.appendChild(bar);
      li.appendChild(count);
      ul.appendChild(li);
    });
  }

  function flagEmoji(cc) {
    if (typeof cc !== "string" || !/^[A-Za-z]{2}$/.test(cc)) return "";
    var u = cc.toUpperCase();
    return String.fromCodePoint(
      0x1f1e6 + u.charCodeAt(0) - 65,
      0x1f1e6 + u.charCodeAt(1) - 65
    );
  }

  /* ---------- subjects table ---------- */

  function renderSubjects(rows) {
    var tbody = $("subjects-body");
    tbody.textContent = "";
    if (!Array.isArray(rows) || rows.length === 0) {
      var tr = document.createElement("tr");
      var td = document.createElement("td");
      td.colSpan = 4;
      td.className = "empty";
      td.textContent = "no data yet";
      tr.appendChild(td);
      tbody.appendChild(tr);
      return;
    }
    rows.forEach(function (r) {
      var tr = document.createElement("tr");
      [r.subject, fmtInt(r.requests), fmtCompact(r.input_tokens), fmtCompact(r.output_tokens)]
        .forEach(function (v) {
          var td = document.createElement("td");
          td.textContent = v == null ? "—" : String(v);
          tr.appendChild(td);
        });
      tbody.appendChild(tr);
    });
  }

  /* ---------- reset countdown ---------- */

  var resetsAtMs = NaN;
  var clockOffset = 0; // serverNow - clientNow

  function tickCountdown() {
    var el = $("reset-note");
    if (!isFinite(resetsAtMs)) return;
    var remain = resetsAtMs - (Date.now() + clockOffset);
    var at = new Date(resetsAtMs);
    var hhmm = String(at.getUTCHours()).padStart(2, "0") + ":" +
               String(at.getUTCMinutes()).padStart(2, "0");
    el.textContent = remain <= 0
      ? "daily budget resetting… (" + hhmm + " UTC)"
      : "daily budget resets in " + fmtDur(remain / 1000) + " (" + hhmm + " UTC)";
  }
  setInterval(tickCountdown, 1000);

  /* ---------- render ---------- */

  function render(d) {
    var st = STATES[d.state] || [String(d.state || "unknown"), "st-off"];
    setPill(st[0], st[1]);
    setText("detail", d.detail || "");
    setText("t-model", d.model || "—");

    var live = d.live || {};
    setText("t-tps", fmtRate(live.tokens_per_sec));
    setText("t-rpm", fmtRate(live.requests_per_min));
    setText("t-live", fmtInt(live.subjects_5m));
    setText("t-flight", fmtInt(live.in_flight));

    var usage = d.usage || {};
    setText("t-today", fmtInt(usage.subjects_today));

    var sys = d.system || {};
    setText("t-uptime", fmtDur(sys.uptime_sec));

    chart.update(live.series);

    var credit = d.credit || {};
    setGauge("g-day", credit.day_remaining_pct);
    setGauge("g-pool", credit.pool_remaining_pct);

    var today = usage.today || {};
    var life = usage.lifetime || {};
    setText("to-req", fmtCompact(today.requests));
    setText("lt-req", fmtCompact(life.requests));
    setText("to-in", fmtCompact(today.input_tokens));
    setText("lt-in", fmtCompact(life.input_tokens));
    setText("to-out", fmtCompact(today.output_tokens));
    setText("lt-out", fmtCompact(life.output_tokens));

    renderBars("endpoints", d.endpoints || [], function (it) { return it.name; });
    renderBars("countries", d.countries || [], function (it) {
      var f = flagEmoji(it.name);
      return (f ? f + " " : "") + it.name;
    });
    renderSubjects(d.top_subjects);

    var kp = d.key_pool || {};
    setText("kp-line",
      fmtInt(kp.active) + " active · " + fmtInt(kp.retired) + " retired · " +
      fmtInt(kp.total) + " total · " + fmtInt(credit.donated_keys) + " donated");

    setText("sys-load", typeof sys.load1 === "number" ? sys.load1.toFixed(2) : "—");
    setText("sys-heap", typeof sys.heap_mb === "number" ? sys.heap_mb.toFixed(1) + " MB" : "—");
    setText("sys-goroutines", fmtInt(sys.goroutines));
    setText("sys-cpu", fmtInt(sys.num_cpu));
    setText("sys-go", sys.go_version || "—");
    setText("sys-version", d.version || "—");

    var lim = d.daily_limits_per_user || {};
    setText("lim-req", fmtInt(lim.requests));
    setText("lim-in", fmtCompact(lim.input_tokens));
    setText("lim-out", fmtCompact(lim.output_tokens));

    var rAt = Date.parse(d.resets_at);
    var sNow = Date.parse(d.now);
    if (isFinite(rAt)) resetsAtMs = rAt;
    if (isFinite(sNow)) clockOffset = sNow - Date.now();
    tickCountdown();
  }

  /* ---------- poll loop with backoff ---------- */

  var POLL_MS = 5000;
  var MAX_BACKOFF_MS = 60000;
  var delay = POLL_MS;
  var hasData = false;

  function setOnline(ok) {
    $("offline").hidden = ok;
    $("dash").classList.toggle("stale", !ok);
    if (!ok) setPill("unreachable", "st-warn");
  }

  function poll() {
    fetch("/v1/status", { cache: "no-store" })
      .then(function (r) {
        if (!r.ok) throw new Error("http " + r.status);
        return r.json();
      })
      .then(function (data) {
        try {
          render(data);
          hasData = true;
        } catch (e) {
          // a render bug must not kill the loop
          if (window.console && console.error) console.error("render:", e);
        }
        delay = POLL_MS;
        setOnline(true);
      })
      .catch(function () {
        delay = Math.min(delay * 2, MAX_BACKOFF_MS);
        setOnline(false);
        if (!hasData) setText("detail", "gateway not responding — it may be down or restarting");
      })
      .then(function () {
        setTimeout(poll, delay);
      });
  }

  poll();
})();
