// Unit tests for the playground's markdown renderer.
//
// The renderer turns untrusted model output into HTML, so most of what is
// protected here is what it must NOT emit: no live <script>, no
// javascript: href, nothing that survived from the model as a tag. The
// rest checks the constructs the playground actually renders, and that a
// half-written buffer mid-stream does not throw.
//
//   node site/md.test.js

const md = require('./md.js');

let failures = 0;
function check(name, ok, detail) {
  if (ok) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ': ' + detail : ''}`);
    failures++;
  }
}
const r = (s) => md.render(s);

console.log('blocks');
check('heading', r('# Title').includes('<h1>Title</h1>'), r('# Title'));
check('h3', r('### Sub').includes('<h3>Sub</h3>'));
check('paragraph', r('just words').includes('<p>just words</p>'));
check('bold', r('a **b** c').includes('<strong>b</strong>'));
check('italic', r('a *b* c').includes('<em>b</em>'), r('a *b* c'));
check('underscore italic', r('a _b_ c').includes('<em>b</em>'), r('a _b_ c'));
check('snake_case is not italic', !r('do_a_thing here').includes('<em>'), r('do_a_thing here'));
check('inline code', r('use `x = 1` now').includes('<code>x = 1</code>'), r('use `x = 1` now'));
check('unordered list', /<ul><li>one<\/li><li>two<\/li><\/ul>/.test(r('- one\n- two')), r('- one\n- two'));
check('ordered list', /<ol><li>one<\/li><li>two<\/li><\/ol>/.test(r('1. one\n2. two')), r('1. one\n2. two'));
check('blockquote', r('> quoted').includes('<blockquote>') && r('> quoted').includes('quoted'));
check('horizontal rule', r('---').includes('<hr>'), r('---'));

console.log('code block with copy button');
const cb = r('```py\nprint(1)\n```');
check('emits a code block', cb.includes('<pre><code'), cb);
check('carries the language', cb.includes('data-lang="py"'), cb);
check('has a copy button', cb.includes('class="pg-codecopy"'), cb);
check('escapes the code body', r('```\n<b>x</b>\n```').includes('&lt;b&gt;x&lt;/b&gt;'), r('```\n<b>x</b>\n```'));

console.log('tables');
const tbl = r('| a | b |\n| --- | --- |\n| 1 | 2 |');
check('renders a table', tbl.includes('<table>') && tbl.includes('<th>a</th>') && tbl.includes('<td>1</td>'), tbl);

console.log('links');
const link = r('see [docs](https://deepseek.com/x)');
check('http link gets an href', link.includes('href="https://deepseek.com/x"'), link);
check('http link is nofollow noopener', link.includes('rel="nofollow noopener noreferrer"'), link);
check('mailto survives', r('[m](mailto:a@b.com)').includes('href="mailto:a@b.com"'), r('[m](mailto:a@b.com)'));
check('relative path survives', r('[d](/docs/)').includes('href="/docs/"'), r('[d](/docs/)'));

console.log('SECURITY: model output is hostile');
const xss = r('<script>alert(1)</script>');
check('a <script> renders inert (escaped)', xss.includes('&lt;script&gt;'), xss);
check('a <script> never appears as a live tag', !xss.includes('<script>'), xss);
const img = r('<img src=x onerror=alert(1)>');
check('a raw <img> is escaped, not emitted', !img.includes('<img') && img.includes('&lt;img'), img);
const jsLink = r('click [here](javascript:alert(1))');
check('a javascript: link produces no javascript: href', !jsLink.includes('javascript:'), jsLink);
check('a javascript: link produces no href at all', !jsLink.includes('href='), jsLink);
check('a javascript: link keeps its text', jsLink.includes('here'), jsLink);
const dataLink = r('[x](data:text/html,<script>alert(1)</script>)');
check('a data: link is dropped', !dataLink.includes('data:') && !dataLink.includes('<script>'), dataLink);
const vb = r('[x](vbscript:msgbox(1))');
check('a vbscript: link is dropped', !vb.includes('vbscript:'), vb);
// A scheme smuggled with whitespace can not even form a link: hrefs stop
// at the first space, so it stays inert text with no href emitted at all.
const sneaky = r('[x](java\tscript:alert(1))');
check('a whitespace-split scheme produces no href', !sneaky.includes('href='), sneaky);
// The href attribute value is always escaped, so a quote can not break out.
const brk = r('[x](https://a.com/"onmouseover="alert(1))');
check('a quote in the href can not break the attribute', !brk.includes('"onmouseover="'), brk);

console.log('STREAMING: partial markdown must not throw');
const partials = [
  '```py\nprint(', '**bold not closed', 'a `code not closed', '| a | b',
  '## ', '> quote\n> more', '- item\n- ', '[link](http', '', '#', '*',
];
for (const p of partials) {
  let ok = true, detail = '';
  try { md.render(p); } catch (e) { ok = false; detail = e.message; }
  check(`renders ${JSON.stringify(p).slice(0, 24)} without throwing`, ok, detail);
}
check('unclosed fence becomes a code block', r('```\nprint(1)').includes('<pre><code'), r('```\nprint(1)'));

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('\nmarkdown renderer is safe');
