// The deepseek playground: the API, in a browser, with no API key.
//
// It is the same free tier the CLI uses – same gateway, same enrolment,
// same quota – so anything you can do here you can do from a shell, and
// the panel under the composer shows you exactly how. That is the point
// of this page. It is not a chat toy; it is a way to find the command.
//
// No framework and no build step, matching the rest of the site. The
// only non-obvious piece is the proof-of-work enrolment, which runs in a
// worker (see pow.js).
(function () {
  'use strict';

  var DEFAULT_GATEWAY = 'https://freeseek.1lm.io';

  // The page lives at /playground/ and this script at the site root, so
  // a bare 'pow-worker.js' would resolve against the wrong directory.
  // Deriving it from this script's own URL keeps the two files together
  // wherever the site is served from, including a local file:// copy.
  var SCRIPT_URL = (document.currentScript && document.currentScript.src) || '';
  var WORKER_URL = SCRIPT_URL
    ? SCRIPT_URL.replace(/playground\.js(\?.*)?$/, 'pow-worker.js')
    : 'pow-worker.js';
  var STORE = {
    token: 'dsplay.token',
    gateway: 'dsplay.gateway',
    controls: 'dsplay.controls',
  };

  // Published rate card, per million tokens, off-peak. Mirrors the CLI's
  // internal/deepseek/pricing.go; the figures shown here are labelled
  // estimates for the same reason they are there.
  //
  // Since 2026-08-16 16:00 UTC the card is time-of-day: 01:00-04:00 and
  // 06:00-10:00 UTC bill at twice these rates. A cost estimate that
  // ignored the clock would be half the truth for seven hours a day, so
  // this reads it, exactly as the CLI does.
  var RATES = { cacheHit: 0.007, cacheMiss: 0.22, output: 0.66 };
  var PEAK_WINDOWS = [[60, 240], [360, 600]];
  var PEAK_MULTIPLIER = 2;

  function peakMultiplier(now) {
    var m = now.getUTCHours() * 60 + now.getUTCMinutes();
    for (var i = 0; i < PEAK_WINDOWS.length; i++) {
      if (m >= PEAK_WINDOWS[i][0] && m < PEAK_WINDOWS[i][1]) return PEAK_MULTIPLIER;
    }
    return 1;
  }

  // The markdown renderer (md.js, loaded just before this script). Answers
  // are markdown and untrusted; md.render escapes every byte the model sent
  // before emitting HTML. If the file somehow did not load we fall back to
  // plain text rather than breaking the page.
  var MD = (typeof dsmd !== 'undefined' && dsmd) || null;

  var store = {
    get: function (k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
    set: function (k, v) { try { localStorage.setItem(k, v); } catch (e) {} },
    del: function (k) { try { localStorage.removeItem(k); } catch (e) {} },
  };

  // --- the four wire formats ---------------------------------------------
  //
  // DeepSeek serves the same model through four request shapes that
  // disagree about almost everything: where the prompt goes, what the
  // output cap is called, and how a streamed delta arrives. The CLI
  // exists partly to hide that; this page exists partly to show it.
  var FORMATS = {
    chat: {
      label: 'chat',
      path: '/chat/completions',
      command: 'chat',
      note: 'OpenAI chat completions. The default, and what most tools speak.',
      build: function (s, history) {
        var msgs = [];
        if (s.system) msgs.push({ role: 'system', content: s.system });
        history.forEach(function (m) { msgs.push({ role: m.role, content: m.content }); });
        var body = { model: s.model, messages: msgs, stream: true, max_tokens: s.maxTokens };
        if (s.temperature !== null) body.temperature = s.temperature;
        applyThinking(body, s);
        return body;
      },
      delta: function (ev) {
        var d = ev.choices && ev.choices[0] && ev.choices[0].delta;
        if (!d) return null;
        if (d.reasoning_content) return { kind: 'reasoning', text: d.reasoning_content };
        if (d.content) return { kind: 'content', text: d.content };
        return null;
      },
    },

    anthropic: {
      label: 'anthropic',
      path: '/anthropic/v1/messages',
      command: 'anthropic',
      note: 'Anthropic Messages – the format Claude Code speaks.',
      build: function (s, history) {
        var body = {
          model: s.model,
          messages: history.map(function (m) { return { role: m.role, content: m.content }; }),
          stream: true,
          max_tokens: s.maxTokens,
        };
        if (s.system) body.system = s.system;
        if (s.temperature !== null) body.temperature = s.temperature;
        applyThinking(body, s);
        return body;
      },
      delta: function (ev) {
        if (ev.type !== 'content_block_delta' || !ev.delta) return null;
        if (ev.delta.type === 'thinking_delta') return { kind: 'reasoning', text: ev.delta.thinking };
        if (ev.delta.type === 'text_delta') return { kind: 'content', text: ev.delta.text };
        return null;
      },
    },

    responses: {
      label: 'responses',
      path: '/responses',
      command: 'respond',
      note: 'OpenAI Responses – the format Codex speaks. Flash only.',
      build: function (s, history) {
        var body = {
          model: s.model,
          input: history[history.length - 1].content,
          stream: true,
          max_output_tokens: s.maxTokens,
        };
        if (s.system) body.instructions = s.system;
        if (s.temperature !== null) body.temperature = s.temperature;
        if (s.search) body.tools = [{ type: 'web_search' }];
        applyThinking(body, s);
        return body;
      },
      delta: function (ev) {
        if (ev.type === 'response.reasoning_text.delta') return { kind: 'reasoning', text: ev.delta };
        if (ev.type === 'response.output_text.delta') return { kind: 'content', text: ev.delta };
        return null;
      },
    },

    fim: {
      label: 'fim',
      path: '/beta/completions',
      command: 'fim',
      note: 'Fill in the middle. No chat structure, never thinks, 4K output cap.',
      fim: true,
      build: function (s) {
        var body = {
          model: s.model,
          prompt: s.prefix,
          stream: true,
          max_tokens: Math.min(s.maxTokens, 4096),
        };
        if (s.suffix) body.suffix = s.suffix;
        if (s.temperature !== null) body.temperature = s.temperature;
        return body;
      },
      delta: function (ev) {
        var t = ev.choices && ev.choices[0] && ev.choices[0].text;
        return t ? { kind: 'content', text: t } : null;
      },
    },
  };

  function applyThinking(body, s) {
    if (s.think === 'off') body.thinking = { type: 'disabled' };
    if (s.effort) body.reasoning_effort = s.effort;
  }

  // Usage arrives in a different place in every format. Measured against
  // the live API; see gateway/internal/meter for the same table in Go.
  function readUsage(ev) {
    var u = ev.usage || (ev.response && ev.response.usage);
    if (!u) return null;
    if (typeof u.prompt_tokens === 'number') {
      return {
        input: u.prompt_tokens,
        cached: u.prompt_cache_hit_tokens || 0,
        output: u.completion_tokens || 0,
        reasoning: (u.completion_tokens_details && u.completion_tokens_details.reasoning_tokens) || 0,
      };
    }
    if (typeof u.input_tokens === 'number') {
      // Anthropic reports input_tokens EXCLUDING cache reads; every other
      // format includes them.
      var cached = u.cache_read_input_tokens ||
        (u.input_tokens_details && u.input_tokens_details.cached_tokens) || 0;
      var input = u.cache_read_input_tokens !== undefined
        ? u.input_tokens + cached + (u.cache_creation_input_tokens || 0)
        : u.input_tokens;
      return {
        input: input,
        cached: cached,
        output: u.output_tokens || 0,
        reasoning: (u.output_tokens_details && u.output_tokens_details.reasoning_tokens) || 0,
      };
    }
    return null;
  }

  function cost(u) {
    if (!u) return 0;
    var miss = Math.max(0, u.input - u.cached);
    var mult = peakMultiplier(new Date());
    return mult * (u.cached * RATES.cacheHit + miss * RATES.cacheMiss + u.output * RATES.output) / 1e6;
  }

  function money(usd) {
    if (usd === 0) return '$0';
    if (usd < 0.0000005) return '<$0.000001';
    if (usd < 0.01) return '$' + usd.toFixed(6);
    if (usd < 1) return '$' + usd.toFixed(4);
    return '$' + usd.toFixed(2);
  }

  function num(n) { return n.toLocaleString('en-US'); }

  // --- element wiring -----------------------------------------------------

  var el = {};
  ['enrol', 'enrolBtn', 'enrolStatus', 'app', 'log', 'composer', 'prompt',
   'send', 'stop', 'usage', 'quota', 'command', 'copy', 'format', 'formatNote',
   'think', 'effort', 'maxTokens', 'temperature', 'system', 'clear', 'reset',
   'gateway', 'fimFields', 'suffix', 'chatFields', 'error',
   'search', 'searchField'
  ].forEach(function (id) { el[id] = document.getElementById('pg-' + id); });

  if (!el.app) return; // not the playground page

  var history = [];
  var controller = null;

  function gateway() {
    return (store.get(STORE.gateway) || DEFAULT_GATEWAY).replace(/\/+$/, '');
  }

  function token() { return store.get(STORE.token); }

  // A format that is not one of the four is a bug somewhere else – a
  // stale localStorage entry, an option removed from the markup – and it
  // must not be able to take the page down. Falling back to chat keeps a
  // working playground instead of a blank panel.
  function formatOf(name) { return FORMATS[name] || FORMATS.chat; }

  function controls() {
    var maxTokens = parseInt(el.maxTokens.value, 10);
    var temp = el.temperature.value === '' ? null : parseFloat(el.temperature.value);
    return {
      format: el.format.value,
      model: 'deepseek-v4-flash',
      think: el.think.value,
      effort: el.effort.value || '',
      maxTokens: isNaN(maxTokens) ? 1024 : Math.max(1, Math.min(4096, maxTokens)),
      temperature: isNaN(temp) ? null : temp,
      system: el.system.value.trim(),
      search: !!(el.search && el.search.checked),
      prefix: el.prompt.value,
      suffix: el.suffix.value,
    };
  }

  function saveControls() {
    var c = controls();
    delete c.prefix;
    delete c.suffix;
    store.set(STORE.controls, JSON.stringify(c));
    renderCommand();
  }

  function restoreControls() {
    var raw = store.get(STORE.controls);
    if (!raw) return;
    try {
      var c = JSON.parse(raw);
      if (c.format && FORMATS[c.format]) el.format.value = c.format;
      if (c.think) el.think.value = c.think;
      if (c.effort !== undefined) el.effort.value = c.effort;
      if (c.maxTokens) el.maxTokens.value = c.maxTokens;
      if (c.temperature !== null && c.temperature !== undefined) el.temperature.value = c.temperature;
      if (c.system) el.system.value = c.system;
      if (c.search && el.search) el.search.checked = true;
    } catch (e) {}
  }

  // --- the equivalent command --------------------------------------------
  //
  // The reason this page exists rather than being a generic chat box. Say
  // what you want here, then take the line away with you.
  function renderCommand() {
    var s = controls();
    var f = formatOf(s.format);
    var parts = ['deepseek', f.command];

    if (f.fim) {
      parts.push(quote(s.prefix || 'def add(a, b):'));
      if (s.suffix) parts.push('--suffix ' + quote(s.suffix));
    } else {
      parts.push(quote(el.prompt.value || 'why is the sky blue'));
    }
    if (s.system) parts.push('--system ' + quote(s.system));
    if (s.think === 'off') parts.push('--think off');
    if (s.effort) parts.push('--effort ' + s.effort);
    if (s.maxTokens !== 1024) parts.push('--max-tokens ' + s.maxTokens);
    if (s.temperature !== null) parts.push('--temperature ' + s.temperature);
    if (s.search && f.command === 'respond') parts.push('--web-search');
    if (history.length > 1 && !f.fim) parts.push('--session playground');

    el.command.textContent = wrap(parts);
    el.formatNote.textContent = f.note;
    el.fimFields.hidden = !f.fim;
    el.chatFields.hidden = !!f.fim;
    if (el.searchField) el.searchField.hidden = f.command !== 'respond';
  }

  function quote(s) {
    if (!s) return '""';
    return '"' + s.replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, ' ') + '"';
  }

  // wrap breaks a long command across lines the way a person would type
  // it, so it stays copy-pasteable.
  function wrap(parts) {
    var line = parts[0] + ' ' + parts[1] + ' ' + parts[2];
    var rest = parts.slice(3);
    if (!rest.length) return line;
    return line + ' \\\n  ' + rest.join(' \\\n  ');
  }

  // --- the browser check (optional) ---------------------------------------

  // Cloudflare Turnstile, present only when the build was given a
  // sitekey: no key means no container, no third-party script, and
  // turnstileAnswer() resolves null without waiting for anything. With a
  // key, the gateway's browser lane wants the widget's answer alongside
  // the solved puzzle (see gateway/internal/server/turnstile.go), so the
  // widget renders as soon as Cloudflare's script loads and the
  // enrolment waits for its token.
  var TS_HOST_ID = 'turnstile';
  var tsState = { rendered: false, token: null, error: null };

  function turnstileHost() {
    return document.getElementById('pg-' + TS_HOST_ID);
  }

  // Named as the onload callback in the api.js script tag.
  window.dsTurnstileOnload = function () {
    var host = turnstileHost();
    if (!host || tsState.rendered || !window.turnstile) return;
    tsState.rendered = true;
    window.turnstile.render(host, {
      sitekey: host.getAttribute('data-sitekey'),
      callback: function (t) { tsState.token = t; tsState.error = null; },
      'expired-callback': function () { tsState.token = null; },
      'error-callback': function (code) { tsState.error = String(code || 'unknown'); },
    });
  };

  // Resolves with the token to send, or null when this build has no
  // browser check. Rejects when the check exists but will not produce a
  // token, which is a real answer the enrolment error path can show.
  function turnstileAnswer() {
    if (!turnstileHost()) return Promise.resolve(null);
    return new Promise(function (resolve, reject) {
      var waited = 0;
      (function poll() {
        if (tsState.token) return resolve(tsState.token);
        if (tsState.error) {
          return reject(new Error('the browser check failed (' + tsState.error +
            '); reload the page and try again'));
        }
        waited += 250;
        if (waited > 60000) {
          return reject(new Error('the browser check did not finish; reload the page and try again'));
        }
        setTimeout(poll, 250);
      })();
    });
  }

  // --- enrolment ----------------------------------------------------------

  function showEnrolled(on) {
    el.enrol.hidden = on;
    el.app.hidden = !on;
  }

  function enrol() {
    el.enrolBtn.disabled = true;
    setEnrolStatus('asking the gateway for a puzzle…');

    fetchJSON('POST', '/v1/anon/challenge', {})
      .then(function (ch) {
        if (ch.algorithm && ch.algorithm !== 'sha256-leading-zero-bits') {
          throw new Error('this page does not implement ' + ch.algorithm + '; try the CLI');
        }
        setEnrolStatus('solving ' + ch.difficulty + ' bits of proof-of-work…');
        return solveInWorker(ch);
      })
      .then(function (solved) {
        if (turnstileHost() && !tsState.token) {
          setEnrolStatus('solved in ' + solved.seconds.toFixed(1) +
            's – waiting for the browser check…');
        }
        return turnstileAnswer().then(function (tsToken) {
          setEnrolStatus('solved in ' + solved.seconds.toFixed(1) + 's (' +
            num(solved.hashes) + ' hashes) – claiming a token…');
          var req = {
            challenge: solved.challenge,
            nonce: String(solved.nonce),
          };
          if (tsToken) req.turnstile_token = tsToken;
          return fetchJSON('POST', '/v1/anon/token', req);
        });
      })
      .then(function (res) {
        store.set(STORE.token, res.token);
        showEnrolled(true);
        setQuota(res.quota);
        el.prompt.focus();
      })
      .catch(function (err) {
        setEnrolStatus('');
        showError(err.message || String(err));
        el.enrolBtn.disabled = false;
      });
  }

  function solveInWorker(ch) {
    return new Promise(function (resolve, reject) {
      var worker;
      try {
        worker = new Worker(WORKER_URL);
      } catch (e) {
        reject(new Error('this browser would not start the solver: ' + e.message));
        return;
      }
      var started = Date.now();
      worker.onmessage = function (e) {
        var m = e.data;
        if (m.type === 'progress') {
          setEnrolStatus('solving ' + ch.difficulty + ' bits – ' + num(m.hashes) + ' hashes…');
          return;
        }
        worker.terminate();
        if (m.type === 'done') {
          resolve({
            challenge: ch.challenge, nonce: m.nonce, hashes: m.hashes,
            seconds: (Date.now() - started) / 1000,
          });
        } else {
          reject(new Error('could not solve the puzzle'));
        }
      };
      worker.onerror = function (e) {
        worker.terminate();
        reject(new Error('the solver failed: ' + (e.message || 'unknown')));
      };
      worker.postMessage({ challenge: ch.challenge, difficulty: ch.difficulty });
    });
  }

  function setEnrolStatus(text) {
    el.enrolStatus.textContent = text;
    el.enrolStatus.hidden = !text;
  }

  // --- talking to the gateway --------------------------------------------

  function fetchJSON(method, path, body) {
    var opts = { method: method, headers: {} };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    var t = token();
    if (t) opts.headers['Authorization'] = 'Bearer ' + t;

    return fetch(gateway() + path, opts).then(function (res) {
      return res.text().then(function (text) {
        var data = null;
        try { data = JSON.parse(text); } catch (e) {}
        if (!res.ok) {
          throw new Error((data && data.error && data.error.message) ||
            ('HTTP ' + res.status + ' from the gateway'));
        }
        return data;
      });
    });
  }

  function send() {
    var s = controls();
    var f = formatOf(s.format);
    var text = f.fim ? s.prefix : el.prompt.value.trim();
    if (!text) return;

    hideError();
    if (!f.fim) {
      history.push({ role: 'user', content: text });
      appendTurn('user', text);
      el.prompt.value = '';
    }

    var turn = appendTurn('assistant', '', !f.fim);
    var started = Date.now();
    setBusy(true);
    renderCommand();

    controller = new AbortController();
    var body = f.build(s, history);
    var t = token();

    fetch(gateway() + f.path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + t },
      body: JSON.stringify(body),
      signal: controller.signal,
    })
      .then(function (res) {
        readQuotaHeaders(res);
        if (!res.ok) {
          return res.text().then(function (text) {
            var data = null;
            try { data = JSON.parse(text); } catch (e) {}
            throw new Error((data && data.error && data.error.message) || ('HTTP ' + res.status));
          });
        }
        return streamInto(res, f, turn, started);
      })
      .catch(function (err) {
        if (err.name === 'AbortError') {
          turn.footer.textContent = 'stopped';
          return;
        }
        showError(err.message || String(err));
        turn.el.remove();
        if (!f.fim) history.pop();
      })
      .then(function () {
        setBusy(false);
        controller = null;
        refreshQuota();
      });
  }

  // streamInto renders server-sent events as they arrive.
  function streamInto(res, f, turn, started) {
    var reader = res.body.getReader();
    var decoder = new TextDecoder();
    var buffer = '';
    var usage = null;
    var content = '';

    function pump() {
      return reader.read().then(function (r) {
        if (r.done) {
          finish();
          return;
        }
        buffer += decoder.decode(r.value, { stream: true });

        var idx;
        while ((idx = buffer.indexOf('\n')) >= 0) {
          var line = buffer.slice(0, idx).replace(/\r$/, '');
          buffer = buffer.slice(idx + 1);
          if (line.slice(0, 5) !== 'data:') continue;
          var payload = line.slice(5).trim();
          if (!payload || payload === '[DONE]') continue;

          var ev;
          try { ev = JSON.parse(payload); } catch (e) { continue; }

          var d = f.delta(ev);
          if (d && d.text) {
            if (d.kind === 'reasoning') {
              turn.reasoning.textContent += d.text;
              turn.reasoningWrap.hidden = false;
            } else {
              content += d.text;
              setTurnBody(turn, content);
            }
            scrollToEnd();
          }
          var u = readUsage(ev);
          if (u) usage = u;
        }
        return pump();
      });
    }

    function finish() {
      if (content && !formatOf(el.format.value).fim) {
        history.push({ role: 'assistant', content: content });
      }
      var seconds = (Date.now() - started) / 1000;
      turn.footer.textContent = usageLine(usage, seconds);
      setUsage(usage, seconds);
      renderCommand();
    }

    return pump();
  }

  // usageLine is the CLI's own stderr line, character for character. Two
  // surfaces reporting the same numbers the same way is most of what
  // makes this page feel like the tool rather than a demo of it.
  function usageLine(u, seconds) {
    if (!u) return '· ' + seconds.toFixed(2) + 's';
    var parts = ['· flash', num(u.input) + ' in'];
    if (u.cached) parts.push(num(u.cached) + ' cached');
    var out = num(u.output) + ' out';
    if (u.reasoning) out += ' (' + num(u.reasoning) + ' think)';
    parts.push(out);
    parts.push('~' + money(cost(u)));
    parts.push(seconds.toFixed(2) + 's');
    return parts.join(' · ');
  }

  function setUsage(u, seconds) {
    el.usage.textContent = usageLine(u, seconds);
  }

  // --- quota --------------------------------------------------------------

  function readQuotaHeaders(res) {
    var left = res.headers.get('X-Free-Requests-Remaining');
    if (left === null) return;
    el.quota.textContent = left + ' requests left today';
  }

  function refreshQuota() {
    if (!token()) return;
    fetchJSON('GET', '/v1/anon/quota').then(setQuota).catch(function () {});
  }

  function setQuota(q) {
    if (!q || !q.limits) return;
    var left = Math.max(0, q.limits.requests - q.used.requests);
    el.quota.textContent = left + '/' + q.limits.requests + ' requests · ' +
      num(Math.max(0, q.limits.output_tokens - q.used.output_tokens)) + ' output tokens left today';
    if (q.service_exhausted) {
      showError('The free tier has run out of credit. Bring your own key and use the CLI: ' +
        'https://platform.deepseek.com/api_keys');
    }
  }

  // --- rendering ----------------------------------------------------------

  // A turn renders the model's markdown into .pg-body. The raw text is kept
  // alongside so the reader can copy it or flip to it: the answer is
  // markdown and the reason to be here is often the exact text, not the
  // prettified version.
  function appendTurn(role, text, useMd) {
    var wrap = document.createElement('div');
    wrap.className = 'pg-turn pg-' + role;

    var label = document.createElement('div');
    label.className = 'pg-role';
    label.textContent = role === 'user' ? '>' : 'deepseek';
    wrap.appendChild(label);

    var reasoningWrap = document.createElement('details');
    reasoningWrap.className = 'pg-reasoning';
    reasoningWrap.hidden = true;
    var summary = document.createElement('summary');
    summary.textContent = 'reasoning';
    reasoningWrap.appendChild(summary);
    var reasoning = document.createElement('pre');
    reasoningWrap.appendChild(reasoning);
    wrap.appendChild(reasoningWrap);

    var body = document.createElement('div');
    body.className = 'pg-body';
    wrap.appendChild(body);

    var rawPre = document.createElement('pre');
    rawPre.className = 'pg-raw';
    rawPre.hidden = true;
    wrap.appendChild(rawPre);

    var turn = {
      el: wrap, body: body, rawPre: rawPre, reasoning: reasoning,
      reasoningWrap: reasoningWrap, useMd: role === 'assistant' && !!useMd && !!MD,
      rawText: '',
    };

    // Only the model's turns get the copy/raw tools; the user's own prompt
    // is already in front of them.
    if (role === 'assistant') {
      var tools = document.createElement('div');
      tools.className = 'pg-turn-tools';
      tools.hidden = true;

      var copyBtn = document.createElement('button');
      copyBtn.type = 'button';
      copyBtn.textContent = 'copy';
      copyBtn.addEventListener('click', function () {
        navigator.clipboard.writeText(turn.rawText).then(function () {
          copyBtn.textContent = 'copied';
          setTimeout(function () { copyBtn.textContent = 'copy'; }, 1200);
        });
      });
      tools.appendChild(copyBtn);

      if (turn.useMd) {
        var rawBtn = document.createElement('button');
        rawBtn.type = 'button';
        rawBtn.textContent = 'raw';
        rawBtn.addEventListener('click', function () {
          var showRaw = turn.rawPre.hidden;
          turn.rawPre.hidden = !showRaw;
          turn.body.hidden = showRaw;
          rawBtn.textContent = showRaw ? 'rendered' : 'raw';
        });
        tools.appendChild(rawBtn);
      }
      turn.tools = tools;
      wrap.appendChild(tools);
    }

    var footer = document.createElement('div');
    footer.className = 'pg-footer';
    wrap.appendChild(footer);
    turn.footer = footer;

    setTurnBody(turn, text || '');

    el.log.appendChild(wrap);
    scrollToEnd();
    return turn;
  }

  // Re-render the whole accumulated buffer. Cheap at chat sizes, and it
  // means partial markdown mid-stream is just the same render on a shorter
  // string – md.render tolerates an unclosed fence or a dangling '*'.
  function setTurnBody(turn, text) {
    turn.rawText = text;
    if (turn.useMd) turn.body.innerHTML = MD.render(text);
    else turn.body.textContent = text;
    turn.rawPre.textContent = text;
    if (turn.tools && text) turn.tools.hidden = false;
  }

  function scrollToEnd() { el.log.scrollTop = el.log.scrollHeight; }

  function setBusy(busy) {
    el.send.disabled = busy;
    el.stop.hidden = !busy;
    el.prompt.disabled = busy;
  }

  function showError(msg) {
    el.error.textContent = msg;
    el.error.hidden = false;
  }

  function hideError() { el.error.hidden = true; }

  // --- boot ---------------------------------------------------------------

  restoreControls();

  el.enrolBtn.addEventListener('click', enrol);
  el.send.addEventListener('click', send);
  el.stop.addEventListener('click', function () { if (controller) controller.abort(); });
  el.copy.addEventListener('click', function () {
    navigator.clipboard.writeText(el.command.textContent).then(function () {
      el.copy.textContent = 'copied';
      setTimeout(function () { el.copy.textContent = 'copy'; }, 1200);
    });
  });
  // Copy buttons inside rendered code blocks. Delegated, because the blocks
  // are re-created on every streamed chunk and binding each one would leak.
  el.log.addEventListener('click', function (e) {
    var t = e.target;
    if (!t || String(t.className || '').indexOf('pg-codecopy') < 0) return;
    var box = t.parentNode;
    var pre = box && box.getElementsByTagName ? box.getElementsByTagName('pre')[0] : null;
    if (!pre) return;
    navigator.clipboard.writeText(pre.textContent).then(function () {
      t.textContent = 'copied';
      setTimeout(function () { t.textContent = 'copy'; }, 1200);
    });
  });

  el.clear.addEventListener('click', function () {
    history = [];
    el.log.innerHTML = '';
    el.usage.textContent = '';
    hideError();
    renderCommand();
  });
  el.reset.addEventListener('click', function () {
    store.del(STORE.token);
    history = [];
    el.log.innerHTML = '';
    showEnrolled(false);
    el.enrolBtn.disabled = false;
    setEnrolStatus('');
  });

  el.prompt.addEventListener('keydown', function (e) {
    // Enter sends, Shift+Enter is a newline – the convention every chat
    // box has trained people into.
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  });
  el.prompt.addEventListener('input', renderCommand);
  el.suffix.addEventListener('input', renderCommand);

  ['format', 'think', 'effort', 'maxTokens', 'temperature', 'system', 'search'].forEach(function (id) {
    if (!el[id]) return;
    el[id].addEventListener('change', saveControls);
    el[id].addEventListener('input', renderCommand);
  });

  if (el.gateway) {
    el.gateway.value = gateway();
    el.gateway.addEventListener('change', function () {
      var v = el.gateway.value.trim().replace(/\/+$/, '');
      if (v && v !== DEFAULT_GATEWAY) store.set(STORE.gateway, v);
      else store.del(STORE.gateway);
      store.del(STORE.token);
      showEnrolled(false);
      el.enrolBtn.disabled = false;
    });
  }

  renderCommand();
  if (token()) {
    showEnrolled(true);
    refreshQuota();
  } else {
    showEnrolled(false);
  }
})();
