import { JSDOM } from "jsdom";
import DOMPurify from "dompurify";

// Set up DOM for DOMPurify before importing ansi utils.
// In the browser DOMPurify auto-detects window; in Node we must provide it.
const dom = new JSDOM("");
const g = globalThis as Record<string, unknown>;
g.window = dom.window;
g.document = dom.window.document;

// DOMPurify in Node requires explicit initialization with a window.
// Monkey-patch so the module-level `DOMPurify.sanitize` works.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const purify = DOMPurify(dom.window as any);
Object.assign(DOMPurify, { sanitize: purify.sanitize.bind(purify) });

import { ansiToHtml, stripAnsi } from "./ansi";

let passed = 0;
let failed = 0;

function assert(condition: boolean, msg: string) {
  if (condition) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

// ansiToHtml: no escape sequences → empty string
assert(ansiToHtml("hello world") === "", "plain text returns empty");
assert(ansiToHtml("") === "", "empty string returns empty");

// ansiToHtml: ANSI codes produce HTML
const html = ansiToHtml("\x1b[32mgreen\x1b[0m");
assert(html.includes("green"), "contains the text 'green'");
assert(html.includes("<span"), "contains a span tag");
assert(html.includes("color"), "contains color styling");
assert(/style="[^"]*color:rgb\(0,187,0\)/.test(html), "green SGR maps to inline rgb style");

// ansiToHtml: bold
const boldHtml = ansiToHtml("\x1b[1mbold\x1b[0m");
assert(boldHtml.includes("bold"), "bold text present");
assert(boldHtml.includes("<"), "contains HTML tags");

// stripAnsi: removes escape sequences
assert(stripAnsi("\x1b[32mgreen\x1b[0m") === "green", "strips green code");
assert(stripAnsi("hello") === "hello", "plain text unchanged");
assert(
  stripAnsi("\x1b[0m\x1b[32mTask\x1b[0m \x1b[36mdev:copy\x1b[0m") === "Task dev:copy",
  "strips deno-style output",
);

// stripAnsi: cursor show/hide sequences
assert(stripAnsi("\x1b[?25lhidden\x1b[?25h") === "hidden", "strips cursor hide/show sequences");

// C1 single-byte CSI (0x9b) is equivalent to ESC [. ansi_up only
// recognizes the two-byte form, so ansiToHtml normalizes 0x9b -> ESC [ and
// colorizes; non-SGR C1 sequences are stripped like their two-byte forms.
const c1Sgr = ansiToHtml("\x9b32mgreen\x9b0m");
assert(c1Sgr.includes("<span"), "C1 SGR: color span preserved after normalization");
assert(c1Sgr.includes("green"), "C1 SGR: text preserved");
assert(!c1Sgr.includes("\x9b"), "C1 SGR: no raw 0x9b in output");
assert(!c1Sgr.includes("&#x9B;"), "C1 SGR: no escaped 0x9b entity");
assert(stripAnsi("\x9b1GDev") === "Dev", "C1 CHA: stripped");

// ansiToHtml: cursor-movement sequences must not leave stray letters.
// The old ansi-to-html library only understood SGR; without pre-stripping,
// \x1b[1G (Cursor Horizontal Absolute) rendered as a stray "G". This is the
// "GGGDev code has changes" regression: logging libraries emit repeated
// \x1b[1G to redraw/align status lines.
//
// When the only escape sequences are non-SGR, ansiToHtml returns "" (the
// component renders stripAnsi(text) in that case). When SGR is also present,
// ansiToHtml returns HTML and must not contain stray letters from the
// cursor sequences. We test both paths.

// Pure non-SGR: ansiToHtml returns "" (nothing to colorize), and the
// companion stripAnsi removes the sequences cleanly.
assert(ansiToHtml("\x1b[1GDev code has changes") === "", "CHA-only: no colorizable HTML");
assert(
  stripAnsi("\x1b[1GDev code has changes") === "Dev code has changes",
  "CHA-only: stripAnsi preserves text",
);

const triple1G = "\x1b[1G\x1b[1G\x1b[1GDev code has changes not yet deployed to production";
assert(ansiToHtml(triple1G) === "", "triple CHA-only: no colorizable HTML");
assert(
  stripAnsi(triple1G) === "Dev code has changes not yet deployed to production",
  "triple CHA-only: stripAnsi has no stray G",
);
assert(!stripAnsi(triple1G).includes("G"), "triple CHA-only: no stray 'G' anywhere");

// SGR + non-SGR mixed: ansiToHtml produces HTML with the color span and the
// plain text, but no stray letters from the cursor sequence.
const colorAndCursor = ansiToHtml("\x1b[32mINFO\x1b[0m\x1b[1GDev code has changes");
assert(colorAndCursor.includes("<span"), "color+CHA: color span preserved");
assert(colorAndCursor.includes("INFO"), "color+CHA: colored text preserved");
assert(!colorAndCursor.includes("GDev"), "color+CHA: no stray 'G'");
assert(colorAndCursor.includes("Dev code has changes"), "color+CHA: plain text preserved");

// Erase-line + cursor-absolute, mixed with color, must not leak 'K' or 'G'.
const erase = ansiToHtml("\x1b[32mok\x1b[0m\x1b[2K\x1b[1GDev");
assert(!erase.includes("K"), "EL+CHA+color: no stray 'K'");
assert(!erase.includes("GDev"), "EL+CHA+color: no stray 'G'");
assert(erase.includes("ok"), "EL+CHA+color: colored text preserved");
assert(erase.includes("Dev"), "EL+CHA+color: plain text preserved");

// Cursor-up then cursor-absolute, mixed with color, must not leak 'A' or 'G'
// from the cursor sequences. Check for the literal artifacts ("2A", "GDev")
// rather than bare letters, which could legitimately appear in markup.
const cuu = ansiToHtml("\x1b[31mwarn\x1b[0m\x1b[2A\x1b[1GDev");
assert(!cuu.includes("2A"), "CUU+CHA+color: no stray '2A'");
assert(!cuu.includes("GDev"), "CUU+CHA+color: no stray 'G'");
assert(cuu.includes("warn"), "CUU+CHA+color: colored text preserved");
assert(cuu.includes("Dev"), "CUU+CHA+color: plain text preserved");

// OSC title sequences are fully removed (no stray ']' or title text).
const osc = ansiToHtml("\x1b]0;my title\x07after");
assert(!osc.includes("title"), "OSC: title removed");
assert(!osc.includes("]"), "OSC: no stray ']'");
// OSC-only input has no SGR, so ansiToHtml returns "".
assert(ansiToHtml("\x1b]0;t\x07x") === "", "OSC-only: no colorizable HTML");
assert(stripAnsi("\x1b]0;t\x07x") === "x", "OSC-only: stripAnsi preserves text");

// ansiToHtml: mixed plain and ANSI
const mixed = ansiToHtml("hello \x1b[31mred\x1b[0m world");
assert(mixed.includes("hello"), "mixed: includes plain text before");
assert(mixed.includes("red"), "mixed: includes colored text");
assert(mixed.includes("world"), "mixed: includes plain text after");

// ansiToHtml: sanitizes malicious HTML in the source
const xss = ansiToHtml("\x1b[32m<script>alert(1)</script>\x1b[0m");
assert(!xss.includes("<script>"), "XSS: script tags are stripped");

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
