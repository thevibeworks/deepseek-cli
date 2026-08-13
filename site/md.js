// A small, hostile-input-safe markdown renderer for the playground.
//
// Model output is markdown and it is untrusted, so this file has one
// non-negotiable job: never let a byte the model sent reach the page as
// HTML. Every tag emitted is written here; every run of model text is
// escaped exactly once before it is emitted, and link targets are dropped
// unless they are http(s) or mailto. There is no path that passes raw
// model HTML through.
//
// It is deliberately small. The playground streams tokens and re-renders
// the whole buffer per chunk, so the renderer also has to tolerate
// half-written markdown mid-stream without throwing: an unclosed fence is
// a code block to end of input, an unmatched `*` is a literal asterisk.
//
// Same dual export shape as pow.js: a browser global, or module.exports
// under node so md.test.js can require it.
(function (global) {
  'use strict';

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  // Only http(s) and mailto survive. A javascript:, data:, vbscript: or
  // file: URL is dropped and the link renders as plain text. A target with
  // no scheme (a relative path or #anchor) is harmless and kept. Control
  // characters are stripped before the scheme test so a smuggled scheme can
  // not slip past it; the value is escaped on the way out regardless, which
  // neutralises entity-encoded schemes too.
  function safeHref(href) {
    var raw = String(href).trim();
    var probe = raw.replace(/[\u0000-\u0020]+/g, '').toLowerCase();
    if (/^(https?:\/\/|mailto:)/.test(probe)) return raw;
    if (/^[a-z][a-z0-9+.\-]*:/.test(probe)) return null;
    return raw;
  }

  // Placeholders for lifted-out spans. Control characters that can not
  // appear in the escaped text and survive escapeHtml untouched.
  var CODE = '\u0000';
  var LINK = '\u0001';

  // Inline spans. Code spans are lifted out first (they suppress every
  // other construct inside them), then links (captured before escaping so
  // the href is clean), then the remaining text is escaped once and
  // emphasis is applied to it.
  function inline(src) {
    var codes = [];
    var text = String(src).replace(/(`+)([\s\S]*?)\1/g, function (m, ticks, code) {
      codes.push('<code>' + escapeHtml(code.replace(/^ | $/g, '')) + '</code>');
      return CODE + (codes.length - 1) + CODE;
    });

    var links = [];
    text = text.replace(/\[([^\]]*)\]\(\s*([^)\s]+)(?:\s+"[^"]*")?\s*\)/g, function (m, label, href) {
      links.push({ label: label, href: safeHref(href) });
      return LINK + (links.length - 1) + LINK;
    });

    text = escapeHtml(text);

    text = text.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    text = text.replace(/__([^_]+)__/g, '<strong>$1</strong>');
    text = text.replace(/\*([^*\s][^*]*?)\*/g, '<em>$1</em>');
    text = text.replace(/(^|[^a-zA-Z0-9_])_([^_]+)_(?![a-zA-Z0-9_])/g, '$1<em>$2</em>');

    text = text.replace(new RegExp(LINK + '(\\d+)' + LINK, 'g'), function (m, n) {
      var l = links[+n];
      var lbl = escapeHtml(l.label);
      if (l.href === null) return lbl;
      return '<a href="' + escapeHtml(l.href) +
        '" rel="nofollow noopener noreferrer" target="_blank">' + lbl + '</a>';
    });

    text = text.replace(new RegExp(CODE + '(\\d+)' + CODE, 'g'), function (m, n) {
      return codes[+n];
    });

    return text;
  }

  function codeBlock(code, lang) {
    return '<div class="pg-code">' +
      '<button class="pg-codecopy" type="button" aria-label="Copy code">copy</button>' +
      '<pre><code' + (lang ? ' data-lang="' + escapeHtml(lang) + '"' : '') + '>' +
      escapeHtml(code) + '</code></pre></div>';
  }

  function splitRow(row) {
    return row.replace(/^\s*\|?/, '').replace(/\|?\s*$/, '').split('|').map(function (c) {
      return c.trim();
    });
  }

  var FENCE_CLOSE = /^\s*(```+|~~~+)\s*$/;

  function render(src) {
    var lines = String(src == null ? '' : src).replace(/\r\n?/g, '\n').split('\n');
    var out = [];
    var i = 0;

    while (i < lines.length) {
      var line = lines[i];

      // Fenced code. An unclosed fence runs to the end of the buffer, which
      // is the common mid-stream case and must not throw.
      var fence = line.match(/^\s*(```+|~~~+)(.*)$/);
      if (fence) {
        var marker = fence[1].charAt(0);
        var buf = [];
        i++;
        while (i < lines.length &&
               !(FENCE_CLOSE.test(lines[i]) && lines[i].replace(/^\s*/, '').charAt(0) === marker)) {
          buf.push(lines[i]);
          i++;
        }
        i++; // consume the closing fence when there is one
        var lang = fence[2].trim().replace(/[^a-zA-Z0-9_+\-]/g, '');
        out.push(codeBlock(buf.join('\n'), lang));
        continue;
      }

      if (/^\s*$/.test(line)) { i++; continue; }

      var h = line.match(/^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/);
      if (h) {
        var level = h[1].length;
        out.push('<h' + level + '>' + inline(h[2]) + '</h' + level + '>');
        i++;
        continue;
      }

      if (/^\s{0,3}([-*_])\s*(\1\s*){2,}$/.test(line)) {
        out.push('<hr>');
        i++;
        continue;
      }

      if (/^\s*>/.test(line)) {
        var qbuf = [];
        while (i < lines.length && /^\s*>/.test(lines[i])) {
          qbuf.push(lines[i].replace(/^\s*>\s?/, ''));
          i++;
        }
        out.push('<blockquote>' + render(qbuf.join('\n')) + '</blockquote>');
        continue;
      }

      // Pipe table: a header row, a separator row of dashes, then body rows.
      if (line.indexOf('|') >= 0 && i + 1 < lines.length &&
          /^\s*\|?[\s:|-]*-[\s:|-]*\|?\s*$/.test(lines[i + 1]) &&
          lines[i + 1].indexOf('|') >= 0) {
        var header = splitRow(line);
        var aligns = splitRow(lines[i + 1]).map(function (c) {
          var l = c.charAt(0) === ':';
          var r = c.charAt(c.length - 1) === ':';
          return r && l ? 'center' : r ? 'right' : l ? 'left' : '';
        });
        i += 2;
        var rows = [];
        while (i < lines.length && lines[i].indexOf('|') >= 0 && !/^\s*$/.test(lines[i])) {
          rows.push(splitRow(lines[i]));
          i++;
        }
        var thead = '<thead><tr>' + header.map(function (c, k) {
          return '<th' + (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : '') + '>' + inline(c) + '</th>';
        }).join('') + '</tr></thead>';
        var tbody = '<tbody>' + rows.map(function (r) {
          return '<tr>' + header.map(function (_, k) {
            return '<td' + (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : '') + '>' + inline(r[k] || '') + '</td>';
          }).join('') + '</tr>';
        }).join('') + '</tbody>';
        out.push('<table>' + thead + tbody + '</table>');
        continue;
      }

      // List. Flat only: nested items render as one level, which is enough
      // for chat output and never throws on ragged indentation.
      var lm = line.match(/^\s*([-*+]|\d+[.)])\s+/);
      if (lm) {
        var ordered = /\d/.test(lm[1]);
        var items = [];
        while (i < lines.length) {
          var im = lines[i].match(/^\s*([-*+]|\d+[.)])\s+(.*)$/);
          if (!im) break;
          items.push('<li>' + inline(im[2]) + '</li>');
          i++;
        }
        var tag = ordered ? 'ol' : 'ul';
        out.push('<' + tag + '>' + items.join('') + '</' + tag + '>');
        continue;
      }

      // Paragraph: run to the next blank line or block starter.
      var pbuf = [];
      while (i < lines.length && !/^\s*$/.test(lines[i]) &&
             !/^\s*(```+|~~~+)/.test(lines[i]) &&
             !/^\s{0,3}#{1,6}\s/.test(lines[i]) &&
             !/^\s*>/.test(lines[i]) &&
             !/^\s*([-*+]|\d+[.)])\s+/.test(lines[i])) {
        pbuf.push(lines[i]);
        i++;
      }
      out.push('<p>' + inline(pbuf.join('\n')).replace(/\n/g, '<br>') + '</p>');
    }

    // No separator between blocks: they are display:block and stack on
    // their own, and a stray '\n' would show through the composer's
    // white-space: pre-wrap as an extra blank line.
    return out.join('');
  }

  var api = { render: render, escapeHtml: escapeHtml, safeHref: safeHref };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  global.dsmd = api;
})(typeof self !== 'undefined' ? self : this);
