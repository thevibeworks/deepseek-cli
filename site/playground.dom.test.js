// Behavioural test for the playground, without a browser.
//
// The page cannot be clicked in CI, so this runs playground.js against a
// DOM shim just large enough for it: elements with the ids the real page
// has, a fetch that serves the gateway's documented responses, and a
// Worker that runs the actual pow.js. Then it clicks the buttons.
//
// What it is really protecting is the streaming path. A wire format
// whose delta field moved would show up here as an empty transcript,
// which is exactly how it would show up to a reader – except here it
// fails the build instead of looking like the model said nothing.
//
//   node site/playground.dom.test.js

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const here = __dirname;
let failures = 0;

function check(name, ok, detail) {
  if (ok) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ': ' + detail : ''}`);
    failures++;
  }
}

// --- the smallest DOM that playground.js will accept --------------------

function makeEl(id) {
  const el = {
    id,
    value: '',
    textContent: '',
    hidden: false,
    disabled: false,
    className: '',
    children: [],
    listeners: {},
    scrollTop: 0,
    scrollHeight: 0,
    style: {},
    _html: '',
    // The real page renders markdown by assigning HTML to innerHTML, so the
    // shim has to store it (not just handle the clear-to-''). text() strips
    // the tags back off, which is what a reader sees.
    set innerHTML(v) {
      if (v === '') { this.children = []; this._html = ''; }
      else this._html = v;
    },
    get innerHTML() {
      return this._html || this.children.map((c) => c.textContent).join('');
    },
    addEventListener(kind, fn) {
      (this.listeners[kind] = this.listeners[kind] || []).push(fn);
    },
    appendChild(c) {
      this.children.push(c);
      c.parent = this;
      return c;
    },
    remove() {
      if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this);
    },
    focus() {},
    fire(kind, ev) {
      (this.listeners[kind] || []).forEach((fn) => fn(ev || {}));
    },
    // Depth-first text, which is how a reader sees the transcript. Rendered
    // markdown lives in _html, so strip its tags and count it too.
    text() {
      const own = this.textContent + (this._html ? this._html.replace(/<[^>]*>/g, '') : '');
      return own + this.children.map((c) => c.text()).join('');
    },
  };
  return el;
}

function buildDocument() {
  const html = fs.readFileSync(path.join(here, 'playground', 'index.html'), 'utf8');
  const ids = [...html.matchAll(/id="(pg-[a-zA-Z]+)"/g)].map((m) => m[1]);
  const byId = {};
  ids.forEach((id) => (byId[id] = makeEl(id)));

  // A <select> reports its first option's value until something changes
  // it. Without this the shim hands back '' where a browser would hand
  // back 'chat', and the test would be exercising a state the page can
  // never actually be in.
  for (const m of html.matchAll(/<select id="(pg-[a-zA-Z]+)">([\s\S]*?)<\/select>/g)) {
    const first = m[2].match(/<option value="([^"]*)"/);
    if (first) byId[m[1]].value = first[1];
  }

  // Same for inputs with a value attribute.
  for (const m of html.matchAll(/<input id="(pg-[a-zA-Z]+)"[^>]*value="([^"]*)"/g)) {
    byId[m[1]].value = m[2];
  }
  return {
    byId,
    document: {
      getElementById: (id) => byId[id] || null,
      createElement: () => makeEl(''),
      currentScript: null,
    },
  };
}

// --- canned gateway ------------------------------------------------------

const CHALLENGE = 'dsgate-protocol-vector.v1';
const DIFFICULTY = 12;

function sse(lines) {
  const text = lines.map((l) => `data: ${l}\n\n`).join('');
  const bytes = Buffer.from(text, 'utf8');
  let offset = 0;
  return {
    // Deliver in small pieces so the line splitter is actually exercised
    // across chunk boundaries, which is where it would really break.
    getReader() {
      return {
        read() {
          if (offset >= bytes.length) return Promise.resolve({ done: true });
          const end = Math.min(offset + 13, bytes.length);
          const slice = new Uint8Array(bytes.subarray(offset, end));
          offset = end;
          return Promise.resolve({ done: false, value: slice });
        },
      };
    },
  };
}

function jsonResponse(obj, headers) {
  return {
    ok: true,
    status: 200,
    headers: { get: (k) => (headers || {})[k] ?? null },
    text: () => Promise.resolve(JSON.stringify(obj)),
  };
}

const calls = [];

function makeFetch(streamLines) {
  return function (url, opts) {
    calls.push({ url, opts });
    const body = opts && opts.body ? JSON.parse(opts.body) : null;

    if (url.endsWith('/v1/anon/challenge')) {
      return Promise.resolve(jsonResponse({
        challenge: CHALLENGE, difficulty: DIFFICULTY,
        algorithm: 'sha256-leading-zero-bits', expires_in: 300,
      }));
    }
    if (url.endsWith('/v1/anon/token')) {
      // The nonce must actually solve the puzzle.
      const pow = require('./pow.js');
      const bits = pow.digestBits(`${body.challenge}:${body.nonce}`);
      if (bits < DIFFICULTY) {
        return Promise.resolve({
          ok: false, status: 400, headers: { get: () => null },
          text: () => Promise.resolve(JSON.stringify({ error: { message: 'bad proof' } })),
        });
      }
      return Promise.resolve(jsonResponse({
        token: 'dsf_test.token', subject: 'SUBJ', tier: 'anon',
        quota: {
          used: { requests: 0, input_tokens: 0, output_tokens: 0 },
          limits: { requests: 30, input_tokens: 60000, output_tokens: 20000 },
        },
      }));
    }
    if (url.endsWith('/v1/anon/quota')) {
      return Promise.resolve(jsonResponse({
        used: { requests: 1, input_tokens: 91, output_tokens: 17 },
        limits: { requests: 30, input_tokens: 60000, output_tokens: 20000 },
      }));
    }
    // A completion, in whichever format was asked for.
    return Promise.resolve({
      ok: true,
      status: 200,
      headers: { get: (k) => (k === 'X-Free-Requests-Remaining' ? '29' : null) },
      body: sse(streamLines),
    });
  };
}

// A Worker that runs the real solver, synchronously, then reports back.
function makeWorker() {
  const pow = require('./pow.js');
  return function Worker() {
    const self = { onmessage: null, onerror: null, terminate() {} };
    self.postMessage = function (job) {
      setTimeout(() => {
        const r = pow.solve(job.challenge, job.difficulty, { limit: 1 << 22 });
        self.onmessage({ data: r ? { type: 'done', nonce: r.nonce, hashes: r.hashes } : { type: 'failed' } });
      }, 0);
    };
    return self;
  };
}

function run(streamLines) {
  const { byId, document } = buildDocument();
  const storage = {};
  const sandbox = {
    document,
    localStorage: {
      getItem: (k) => (k in storage ? storage[k] : null),
      setItem: (k, v) => (storage[k] = String(v)),
      removeItem: (k) => delete storage[k],
    },
    fetch: makeFetch(streamLines),
    Worker: makeWorker(),
    AbortController,
    TextDecoder,
    setTimeout,
    console,
    navigator: { clipboard: { writeText: () => Promise.resolve() } },
    Promise,
    Date,
    Math,
    JSON,
    parseInt,
    parseFloat,
    isNaN,
    String,
    Number,
    require,
  };
  sandbox.self = sandbox;
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  // md.js loads before playground.js on the real page and publishes the
  // renderer as a global; do the same here so the page can find it.
  vm.runInContext(fs.readFileSync(path.join(here, 'md.js'), 'utf8'), sandbox, { filename: 'md.js' });
  const src = fs.readFileSync(path.join(here, 'playground.js'), 'utf8');
  vm.runInContext(src, sandbox, { filename: 'playground.js' });
  return { byId, storage, sandbox };
}

const tick = () => new Promise((r) => setTimeout(r, 5));

async function main() {
  // --- enrolment --------------------------------------------------------
  console.log('enrolment');
  const chat = run([
    '{"choices":[{"delta":{"reasoning_content":"thinking about light"}}]}',
    '{"choices":[{"delta":{"content":"The sky is blue "}}]}',
    '{"choices":[{"delta":{"content":"because of scattering."}}]}',
    '{"model":"deepseek-v4-flash","choices":[{"delta":{},"finish_reason":"stop"}],' +
      '"usage":{"prompt_tokens":91,"completion_tokens":17,"total_tokens":108,' +
      '"prompt_cache_hit_tokens":40,"completion_tokens_details":{"reasoning_tokens":6}}}',
    '[DONE]',
  ]);

  check('starts unenrolled', chat.byId['pg-app'].hidden && !chat.byId['pg-enrol'].hidden);

  chat.byId['pg-enrolBtn'].fire('click');
  for (let i = 0; i < 60 && !chat.storage['dsplay.token']; i++) await tick();

  check('solves the puzzle and stores a token', chat.storage['dsplay.token'] === 'dsf_test.token');
  check('reveals the app', !chat.byId['pg-app'].hidden && chat.byId['pg-enrol'].hidden);

  // --- a streamed turn --------------------------------------------------
  console.log('a streamed chat turn');
  chat.byId['pg-prompt'].value = 'why is the sky blue';
  chat.byId['pg-send'].fire('click');
  for (let i = 0; i < 200 && chat.byId['pg-send'].disabled; i++) await tick();

  const log = chat.byId['pg-log'];
  const transcript = log.children.map((c) => c.text()).join('\n');
  check('the question is shown', transcript.includes('why is the sky blue'));
  check('the answer is assembled from the deltas',
    transcript.includes('The sky is blue because of scattering.'), transcript);
  check('reasoning is captured separately', transcript.includes('thinking about light'));

  // The usage line is the CLI's, character for character.
  const usage = chat.byId['pg-usage'].textContent;
  check('usage counts the prompt', usage.includes('91 in'), usage);
  check('usage reports the cache hit', usage.includes('40 cached'), usage);
  check('usage separates reasoning tokens', usage.includes('17 out (6 think)'), usage);
  check('usage prices it', /~\$0\.0000\d\d/.test(usage), usage);

  check('quota came off the response headers',
    chat.byId['pg-quota'].textContent.length > 0, chat.byId['pg-quota'].textContent);

  // --- the command panel -------------------------------------------------
  console.log('the equivalent command');
  const cmd = chat.byId['pg-command'].textContent;
  check('names the binary and subcommand', cmd.startsWith('deepseek chat '), cmd);
  check('carries the prompt', cmd.includes('"why is the sky blue"'), cmd);
  check('a second turn suggests a session', cmd.includes('--session playground'), cmd);

  chat.byId['pg-think'].value = 'off';
  chat.byId['pg-effort'].value = 'low';
  chat.byId['pg-maxTokens'].value = '256';
  chat.byId['pg-format'].fire('change');
  const cmd2 = chat.byId['pg-command'].textContent;
  check('reflects thinking off', cmd2.includes('--think off'), cmd2);
  check('reflects effort', cmd2.includes('--effort low'), cmd2);
  check('reflects max tokens', cmd2.includes('--max-tokens 256'), cmd2);

  // --- the other wire formats -------------------------------------------
  console.log('anthropic deltas');
  const anth = run([
    '{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}',
    '{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}',
    '{"type":"message_delta","delta":{"stop_reason":"end_turn"},' +
      '"usage":{"input_tokens":85,"cache_read_input_tokens":0,"output_tokens":5}}',
  ]);
  anth.byId['pg-enrolBtn'].fire('click');
  for (let i = 0; i < 60 && !anth.storage['dsplay.token']; i++) await tick();
  anth.byId['pg-format'].value = 'anthropic';
  anth.byId['pg-format'].fire('change');
  anth.byId['pg-prompt'].value = 'say OK';
  anth.byId['pg-send'].fire('click');
  for (let i = 0; i < 200 && anth.byId['pg-send'].disabled; i++) await tick();

  const anthText = anth.byId['pg-log'].children.map((c) => c.text()).join('\n');
  check('renders text_delta', anthText.includes('OK'), anthText);
  check('renders thinking_delta', anthText.includes('hmm'), anthText);
  check('posts to the Messages endpoint',
    calls.some((c) => c.url.endsWith('/anthropic/v1/messages')));
  check('command switches subcommand',
    anth.byId['pg-command'].textContent.startsWith('deepseek anthropic '),
    anth.byId['pg-command'].textContent);

  console.log('responses deltas');
  const resp = run([
    '{"type":"response.reasoning_text.delta","delta":"weighing"}',
    '{"type":"response.output_text.delta","delta":"done"}',
    '{"type":"response.completed","response":{"model":"deepseek-v4-flash",' +
      '"usage":{"input_tokens":85,"input_tokens_details":{"cached_tokens":0},"output_tokens":16}}}',
  ]);
  resp.byId['pg-enrolBtn'].fire('click');
  for (let i = 0; i < 60 && !resp.storage['dsplay.token']; i++) await tick();
  resp.byId['pg-format'].value = 'responses';
  resp.byId['pg-format'].fire('change');
  resp.byId['pg-prompt'].value = 'hi';
  resp.byId['pg-send'].fire('click');
  for (let i = 0; i < 200 && resp.byId['pg-send'].disabled; i++) await tick();
  const respText = resp.byId['pg-log'].children.map((c) => c.text()).join('\n');
  check('renders output_text.delta', respText.includes('done'), respText);
  check('renders reasoning_text.delta', respText.includes('weighing'), respText);

  // --- markdown rendering, and its safety net ---------------------------
  //
  // The answer is markdown and the model is not trusted, so the two things
  // that matter most are that it renders and that it can not inject: a
  // <script> in the answer must be inert and a javascript: link must not
  // become an href. Both are unit-tested in md.test.js; here we prove the
  // page actually routes the answer through the renderer on the way in.
  console.log('the answer renders as markdown, safely');
  const mdrun = run([
    '{"choices":[{"delta":{"content":"# Title\\n\\n"}}]}',
    '{"choices":[{"delta":{"content":"Some **bold** and `inline`.\\n\\n"}}]}',
    '{"choices":[{"delta":{"content":"```js\\nalert(1)\\n```\\n\\n"}}]}',
    '{"choices":[{"delta":{"content":"<script>alert(2)</script> and "}}]}',
    '{"choices":[{"delta":{"content":"[x](javascript:alert(3))"}}]}',
    '{"model":"deepseek-v4-flash","choices":[{"delta":{},"finish_reason":"stop"}],' +
      '"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}',
    '[DONE]',
  ]);
  mdrun.byId['pg-enrolBtn'].fire('click');
  for (let i = 0; i < 60 && !mdrun.storage['dsplay.token']; i++) await tick();
  mdrun.byId['pg-prompt'].value = 'render markdown';
  mdrun.byId['pg-send'].fire('click');
  for (let i = 0; i < 200 && mdrun.byId['pg-send'].disabled; i++) await tick();

  const mlog = mdrun.byId['pg-log'];
  const answer = mlog.children[mlog.children.length - 1];
  const bodyEl = answer.children.find((c) => c.className === 'pg-body');
  const rendered = bodyEl ? bodyEl.innerHTML : '';
  check('renders a heading', rendered.includes('<h1>Title</h1>'), rendered);
  check('renders bold', rendered.includes('<strong>bold</strong>'), rendered);
  check('renders inline code', rendered.includes('<code>inline</code>'), rendered);
  check('renders a fenced code block with a copy button',
    rendered.includes('class="pg-codecopy"') && rendered.includes('<pre><code'), rendered);
  check('a <script> in the answer is inert',
    rendered.includes('&lt;script&gt;') && rendered.indexOf('<script>') < 0, rendered);
  check('a javascript: link produces no javascript: href',
    rendered.indexOf('javascript:') < 0, rendered);
  const rawEl = answer.children.find((c) => c.className === 'pg-raw');
  check('keeps the raw markdown for copying',
    !!rawEl && rawEl.textContent.includes('# Title'), rawEl && rawEl.textContent);
  const toolsEl = answer.children.find((c) => c.className === 'pg-turn-tools');
  check('offers copy and raw tools', !!toolsEl && !toolsEl.hidden && toolsEl.children.length === 2,
    toolsEl && toolsEl.children.length);

  // --- refusals ----------------------------------------------------------
  console.log('a gateway refusal reaches the reader');
  const refused = run([]);
  refused.byId['pg-enrolBtn'].fire('click');
  for (let i = 0; i < 60 && !refused.storage['dsplay.token']; i++) await tick();
  refused.sandbox.fetch = function () {
    return Promise.resolve({
      ok: false, status: 429, headers: { get: () => null },
      text: () => Promise.resolve(JSON.stringify({
        error: { message: 'daily requests limit reached. It resets at 00:00 UTC', type: 'free_tier_quota' },
      })),
    });
  };
  refused.byId['pg-prompt'].value = 'one more';
  refused.byId['pg-send'].fire('click');
  for (let i = 0; i < 200 && refused.byId['pg-send'].disabled; i++) await tick();
  check('the gateway\'s own message is shown',
    refused.byId['pg-error'].textContent.includes('00:00 UTC'),
    refused.byId['pg-error'].textContent);
  check('the error panel is revealed', refused.byId['pg-error'].hidden === false);
  check('the failed turn is removed from the transcript',
    refused.byId['pg-log'].children.length === 1,
    `${refused.byId['pg-log'].children.length} turns left`);

  if (failures) {
    console.error(`\n${failures} failure(s)`);
    process.exit(1);
  }
  console.log('\nplayground behaves');
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
