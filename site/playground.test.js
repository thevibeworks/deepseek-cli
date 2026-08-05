// Static checks on the playground.
//
// The page is the one part of this site that is an application, and its
// failure mode is unlike the rest: an element id that does not match
// between the generator and the script throws on load and leaves a blank
// panel with nothing in the console to point at. `build.py --check`
// cannot see that, because both files are individually fine.
//
// So this checks the seam between them, plus syntax on both scripts.
//
//   node site/playground.test.js

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const here = __dirname;
let failures = 0;

function check(name, ok, detail) {
  if (ok) {
    console.log(`  ok   ${name}`);
  } else {
    console.log(`  FAIL ${name}${detail ? ': ' + detail : ''}`);
    failures++;
  }
}

const js = fs.readFileSync(path.join(here, 'playground.js'), 'utf8');
const html = fs.readFileSync(path.join(here, 'playground', 'index.html'), 'utf8');

console.log('scripts parse');
for (const file of ['playground.js', 'pow.js', 'pow-worker.js']) {
  const src = fs.readFileSync(path.join(here, file), 'utf8');
  let ok = true, detail = '';
  try {
    new vm.Script(src, { filename: file });
  } catch (e) {
    ok = false;
    detail = e.message;
  }
  check(file, ok, detail);
}

// Every id the script wires up has to exist in the generated markup.
console.log('every element the script wires up exists in the page');
const listMatch = js.match(/\[\s*((?:'[a-zA-Z]+',?\s*)+)\]\s*\.forEach\(function \(id\) \{\s*el\[id\]/);
if (!listMatch) {
  check('found the id list in playground.js', false, 'the lookup loop changed shape');
} else {
  const ids = listMatch[1].match(/'([a-zA-Z]+)'/g).map((s) => s.slice(1, -1));
  check('id list is non-trivial', ids.length > 10, `${ids.length} ids`);
  for (const id of ids) {
    check(`#pg-${id}`, html.includes(`id="pg-${id}"`));
  }
}

// And the reverse: markup that nothing wires up is dead weight, and
// usually means a rename that only got done on one side.
console.log('every pg- element in the page is used by the script');
const inPage = [...html.matchAll(/id="pg-([a-zA-Z]+)"/g)].map((m) => m[1]);
for (const id of inPage) {
  check(`#pg-${id} is referenced`, js.includes(`'${id}'`));
}

console.log('the page loads its script and the worker is reachable');
check('page references playground.js', html.includes('playground.js'));
check('worker file exists', fs.existsSync(path.join(here, 'pow-worker.js')));
check(
  'worker path is derived from the script url, not hardcoded',
  js.includes('WORKER_URL') && !js.includes("new Worker('pow-worker.js')")
);

// The gateway default has to match the CLI's, or the two halves enrol
// against different services and the page silently stops working when
// the CLI's default moves.
console.log('the browser and the CLI agree on the default gateway');
const goSrc = fs.readFileSync(path.join(here, '..', 'internal', 'deepseek', 'free.go'), 'utf8');
const goDefault = (goSrc.match(/DefaultGatewayURL = "([^"]+)"/) || [])[1];
const jsDefault = (js.match(/DEFAULT_GATEWAY = '([^']+)'/) || [])[1];
check('both are set', Boolean(goDefault && jsDefault), `go=${goDefault} js=${jsDefault}`);
check('they match', goDefault === jsDefault, `go=${goDefault} js=${jsDefault}`);

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('\nplayground wiring is consistent');
