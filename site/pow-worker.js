// The proof-of-work search, off the main thread.
//
// It runs for about a second at the shipped difficulty and for
// considerably longer at an escalated one. On the main thread that would
// freeze the page, and a frozen page is indistinguishable from a broken
// one – so the search lives here and reports progress as it goes.
importScripts('pow.js');

self.onmessage = function (e) {
  var job = e.data || {};
  var result = DSPoW.solve(job.challenge, job.difficulty, {
    limit: job.limit || (1 << 30),
    onProgress: function (hashes) {
      self.postMessage({ type: 'progress', hashes: hashes });
    },
  });
  self.postMessage(
    result
      ? { type: 'done', nonce: result.nonce, hashes: result.hashes }
      : { type: 'failed' }
  );
};
