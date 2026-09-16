import { AnsiUp } from "ansi_up";
import DOMPurify from "dompurify";

// Regexes describing complete ANSI escape sequences (ECMA-48). We strip every
// sequence except SGR color codes (\x1b[...m) before handing the text to
// ansi_up, which turns the remaining SGR codes into styled <span>s. ansi_up
// parses complete CSI sequences (including their final bytes, unlike the old
// ansi-to-html library whose catch-all left stray letters like "GGGDev code
// has changes"), but it still has two gaps this pre-pass covers: generic
// OSC-with-BEL sequences (e.g. \x1b]0;title\x07) leak their payload as
// visible text, and the single-byte C1 CSI control 0x9b isn't recognized.
// Stripping non-SGR sequences up front closes those gaps and acts as
// defense in depth against other unrecognized sequences.
/* eslint-disable no-control-regex */
// OSC and other "string" sequences (DCS/SOS/PM/APC): ESC ]|P|X|^|_ ... BEL|ST.
const STR_SEQ = /[\x1b\x9b][\]PX^_][^\x07\x1b]*(?:\x07|\x1b\\)/g;
// CSI: ESC [ (or the single-byte C1 control 0x9b), then parameter bytes
// [0x30-0x3f], intermediate bytes [0x20-0x2f], and one final byte [0x40-0x7e].
const CSI_SEQ = /(?:\x1b\[|\x9b)[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]/g;
// Other ESC sequences (charset designation, index, reverse index, save/restore
// cursor, keypad mode, ...): ESC + intermediates [0x20-0x2f] + final
// [0x30-0x7e]. The negative lookaheads exclude the CSI/OSC/string introducers
// so this never re-matches what STR_SEQ/CSI_SEQ already consumed.
const ESC_OTHER = /\x1b(?![\]PX^_])(?!\[)[\x20-\x2f]*[\x30-\x7e]/g;
/* eslint-enable no-control-regex */

// stripNonSgr removes every ANSI escape sequence except SGR (\x1b[...m),
// which is kept so ansi_up can turn it into styled <span>s.
function stripNonSgr(text: string): string {
  return text
    .replace(STR_SEQ, "")
    .replace(CSI_SEQ, (m) => (/m$/.test(m) ? m : ""))
    .replace(ESC_OTHER, "");
}

/**
 * Converts a string containing ANSI escape sequences into sanitized HTML.
 * Returns the empty string if no escape sequences are present.
 */
export function ansiToHtml(text: string): string {
  // Fast path: no escape sequences present
  // eslint-disable-next-line no-control-regex
  if (!/[\x1b\x9b]/.test(text)) {
    return "";
  }
  // Drop non-SGR sequences (cursor moves, erases, OSC titles, ...) so ansi_up
  // only sees SGR color codes (ansi_up leaks generic OSC-BEL payloads as
  // visible text). Also normalize the single-byte C1 CSI control (0x9b) to
  // the two-byte ESC [ form, since ansi_up only recognizes the latter.
  const cleaned = stripNonSgr(text).replace(/\x9b/g, "\x1b[");
  // eslint-disable-next-line no-control-regex
  if (!/[\x1b\x9b]/.test(cleaned)) {
    return "";
  }
  // Create a fresh converter each call: ansi_up is stateful/streaming by
  // design, so reusing one would leak SGR state between calls. Defaults are
  // what we want (escape_html: true for XSS safety; use_classes: false so
  // inline styles are emitted, which is all DOMPurify allows below).
  const au = new AnsiUp();
  const raw = au.ansi_to_html(cleaned);
  return DOMPurify.sanitize(raw, {
    ALLOWED_TAGS: ["span", "br", "b", "i", "u"],
    ALLOWED_ATTR: ["style"],
  });
}

/**
 * Strips all ANSI escape sequences from a string.
 */
export function stripAnsi(text: string): string {
  return text.replace(STR_SEQ, "").replace(CSI_SEQ, "").replace(ESC_OTHER, "");
}
