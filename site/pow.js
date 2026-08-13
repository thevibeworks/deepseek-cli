// Proof of work for the deepseek free tier, in the browser.
//
// This is the third implementation of the same puzzle. The gateway
// verifies it (gateway/internal/token), the CLI solves it
// (internal/deepseek/free.go), and this solves it again for the
// playground. None of the three shares code with the others, so all
// three are pinned to one table of test vectors – see pow.test.js, which
// runs under node in `make check`, and the identical tables in the two
// Go suites.
//
// The rule, from the gateway's own challenge response:
//
//   find a decimal nonce where sha256 of the ASCII string
//   "<challenge>:<nonce>" begins with at least <difficulty> zero bits
//
// SHA-256 is implemented here rather than called through WebCrypto on
// purpose. crypto.subtle.digest is asynchronous and its per-call
// overhead dwarfs the hash itself for a 70-byte input; a million of them
// would take minutes. A plain synchronous implementation does the same
// million in about a second, which is the budget this puzzle was sized
// for.
(function (global) {
  'use strict';

  var K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1,
    0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
    0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786,
    0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147,
    0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
    0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
    0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a,
    0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
    0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
  ]);

  // Scratch reused across calls. The solver runs this millions of times
  // and per-iteration allocation would dominate the hash.
  var W = new Uint32Array(64);

  // sha256First is the digest's first two 32-bit words. Nothing here
  // needs the other six: the puzzle only ever inspects leading bits, and
  // this runs in the hot loop.
  function sha256First(msg, len) {
    var h0 = 0x6a09e667, h1 = 0xbb67ae85, h2 = 0x3c6ef372, h3 = 0xa54ff53a,
        h4 = 0x510e527f, h5 = 0x9b05688c, h6 = 0x1f83d9ab, h7 = 0x5be0cd19;

    // Padding: 0x80, zeroes, then the bit length as a 64-bit big-endian.
    var withPad = len + 1 + 8;
    var blocks = Math.ceil(withPad / 64);
    var total = blocks * 64;

    for (var block = 0; block < blocks; block++) {
      var base = block * 64;
      for (var i = 0; i < 16; i++) {
        var p = base + i * 4;
        W[i] = (byteAt(msg, len, p, total) << 24) |
               (byteAt(msg, len, p + 1, total) << 16) |
               (byteAt(msg, len, p + 2, total) << 8) |
               byteAt(msg, len, p + 3, total);
      }
      for (var t = 16; t < 64; t++) {
        var w15 = W[t - 15], w2 = W[t - 2];
        var s0 = ((w15 >>> 7) | (w15 << 25)) ^ ((w15 >>> 18) | (w15 << 14)) ^ (w15 >>> 3);
        var s1 = ((w2 >>> 17) | (w2 << 15)) ^ ((w2 >>> 19) | (w2 << 13)) ^ (w2 >>> 10);
        W[t] = (W[t - 16] + s0 + W[t - 7] + s1) | 0;
      }

      var a = h0, b = h1, c = h2, d = h3, e = h4, f = h5, g = h6, h = h7;
      for (var j = 0; j < 64; j++) {
        var S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
        var ch = (e & f) ^ (~e & g);
        var temp1 = (h + S1 + ch + K[j] + W[j]) | 0;
        var S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
        var maj = (a & b) ^ (a & c) ^ (b & c);
        var temp2 = (S0 + maj) | 0;
        h = g; g = f; f = e; e = (d + temp1) | 0;
        d = c; c = b; b = a; a = (temp1 + temp2) | 0;
      }
      h0 = (h0 + a) | 0; h1 = (h1 + b) | 0; h2 = (h2 + c) | 0; h3 = (h3 + d) | 0;
      h4 = (h4 + e) | 0; h5 = (h5 + f) | 0; h6 = (h6 + g) | 0; h7 = (h7 + h) | 0;
    }
    return [h0 >>> 0, h1 >>> 0];
  }

  // byteAt reads the padded message without materialising the padding.
  function byteAt(msg, len, i, total) {
    if (i < len) return msg[i];
    if (i === len) return 0x80;
    if (i < total - 8) return 0;
    // The last eight bytes are the length in bits, big-endian. Messages
    // here are tens of bytes, so only the low four can be non-zero.
    var shift = (total - 1 - i) * 8;
    var bits = len * 8;
    if (shift >= 32) return 0;
    return (bits >>> shift) & 0xff;
  }

  function leadingZeroBits(words) {
    var n = 0;
    for (var i = 0; i < words.length; i++) {
      var w = words[i];
      if (w !== 0) return n + Math.clz32(w);
      n += 32;
    }
    return n;
  }

  // digestBits returns the leading zero bit count of sha256(text).
  function digestBits(text) {
    var bytes = encodeASCII(text);
    return leadingZeroBits(sha256First(bytes, bytes.length));
  }

  function encodeASCII(s) {
    var out = new Uint8Array(s.length);
    for (var i = 0; i < s.length; i++) out[i] = s.charCodeAt(i) & 0xff;
    return out;
  }

  // solve searches for a nonce, reporting progress and yielding nothing:
  // it is meant to run inside a worker, where blocking is the point.
  //
  // onProgress is called every `reportEvery` hashes so the page can show
  // the search moving. A second of a frozen button is indistinguishable
  // from a broken one.
  function solve(challenge, difficulty, opts) {
    opts = opts || {};
    var limit = opts.limit || 1 << 30;
    var reportEvery = opts.reportEvery || 20000;
    var onProgress = opts.onProgress;

    // The message is "<challenge>:<nonce>". Everything up to the colon is
    // fixed, so it is encoded once and only the digits are rewritten.
    var prefix = encodeASCII(challenge + ':');
    var buf = new Uint8Array(prefix.length + 20);
    buf.set(prefix);

    for (var nonce = 0; nonce < limit; nonce++) {
      var len = writeDecimal(buf, prefix.length, nonce);
      var words = sha256First(buf, len);
      if (leadingZeroBits(words) >= difficulty) {
        if (onProgress) onProgress(nonce + 1);
        return { nonce: nonce, hashes: nonce + 1 };
      }
      if (onProgress && nonce % reportEvery === 0 && nonce > 0) onProgress(nonce);
    }
    return null;
  }

  // writeDecimal renders n into buf at offset and returns the new length.
  function writeDecimal(buf, offset, n) {
    if (n === 0) {
      buf[offset] = 48;
      return offset + 1;
    }
    var start = offset, end = offset;
    while (n > 0) {
      buf[end++] = 48 + (n % 10);
      n = Math.floor(n / 10);
    }
    var len = end;
    // Written backwards; reverse in place.
    for (var i = start, j = end - 1; i < j; i++, j--) {
      var t = buf[i]; buf[i] = buf[j]; buf[j] = t;
    }
    return len;
  }

  var api = { solve: solve, digestBits: digestBits, leadingZeroBits: leadingZeroBits };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  global.DSPoW = api;
})(typeof self !== 'undefined' ? self : this);
