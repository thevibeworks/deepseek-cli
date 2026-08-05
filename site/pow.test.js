// Contract test for the browser proof-of-work solver.
//
// The same puzzle is implemented three times, in three languages, none
// of which imports the others: the gateway verifies it, the CLI solves
// it, and site/pow.js solves it for the playground. This table is the
// contract, and the identical one appears in
// gateway/internal/token/token_test.go and
// internal/deepseek/free_test.go.
//
// Without this, a bug in the browser SHA-256 would surface as "the
// playground never finishes enrolling" with nothing to point at.
//
//   node site/pow.test.js

const crypto = require('crypto');
const pow = require('./pow.js');

let failures = 0;

function check(name, ok, detail) {
  if (ok) {
    console.log(`  ok   ${name}`);
  } else {
    console.log(`  FAIL ${name}${detail ? ': ' + detail : ''}`);
    failures++;
  }
}

// Shared vectors: the smallest nonce whose sha256("<challenge>:<nonce>")
// has at least `difficulty` leading zero bits.
const VECTORS = [
  { challenge: 'dsgate-protocol-vector.v1', difficulty: 8, nonce: 148 },
  { challenge: 'dsgate-protocol-vector.v1', difficulty: 12, nonce: 2601 },
  { challenge: 'dsgate-protocol-vector.v1', difficulty: 16, nonce: 28337 },
  { challenge: 'abc.def', difficulty: 8, nonce: 125 },
  { challenge: 'abc.def', difficulty: 12, nonce: 1917 },
];

console.log('shared proof-of-work vectors');
for (const v of VECTORS) {
  const got = pow.solve(v.challenge, v.difficulty, { limit: 1 << 22 });
  check(
    `${v.challenge} @ ${v.difficulty} bits`,
    got && got.nonce === v.nonce,
    got ? `found ${got.nonce}, want ${v.nonce}` : 'no solution found'
  );
}

// The hand-rolled SHA-256 has to agree with a real one. A solver that is
// internally consistent but wrong would pass the vectors only by
// coincidence — and would fail against the gateway every time.
console.log('sha256 agrees with node crypto');
const samples = [
  '',
  'a',
  'abc',
  'abc.def:1917',
  'x'.repeat(55), // one block, maximal
  'x'.repeat(56), // spills into a second block
  'x'.repeat(63),
  'x'.repeat(64), // exactly one block, so padding adds another
  'x'.repeat(65),
  'x'.repeat(119),
  'x'.repeat(120),
  'dsgate-protocol-vector.v1:28337',
  'ZmFrZS1jaGFsbGVuZ2UtcGF5bG9hZA.c2lnbmF0dXJl:4294967295',
];
for (const s of samples) {
  const want = crypto.createHash('sha256').update(s, 'latin1').digest();
  // Count leading zero bits of the reference digest the slow, obvious way.
  let wantBits = 0;
  for (const byte of want) {
    if (byte !== 0) {
      wantBits += Math.clz32(byte) - 24;
      break;
    }
    wantBits += 8;
  }
  const gotBits = pow.digestBits(s);
  check(
    `sha256(${s.length > 24 ? s.slice(0, 21) + '...' : JSON.stringify(s)})`,
    gotBits === wantBits,
    `leading zero bits ${gotBits}, want ${wantBits}`
  );
}

// The solver must not claim success on a puzzle it did not solve.
console.log('bounded search');
check('gives up rather than looping forever', pow.solve('nope', 40, { limit: 5000 }) === null);

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log('\nall proof-of-work vectors match');
