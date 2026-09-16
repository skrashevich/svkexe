// Pure text reflow for the vim `gq`/`gw` operators. monaco-vim (a port of
// CodeMirror's vim keymap) doesn't implement gq at all, so `gqip` silently
// did nothing. This module holds the framework-free reflow logic; the
// operator registration lives in services/monaco.ts.
//
// The comment-leader chunking and the fill loop are adapted from VSCodeVim's
// reflow operator (https://github.com/VSCodeVim/Vim, MIT, (c) 2015
// VSCodeVim), with two deliberate departures: leaders are only recognised in
// code languages (VSCodeVim treats `# Heading` and `* bullet` in Markdown as
// comments), and prose languages instead get vim's Markdown ftplugin
// behaviour — list items with hanging indent and `>` blockquote leaders.
//
// Semantics, following vim's `gq` for the common cases:
//   * blank (whitespace-only) lines separate paragraphs and are preserved
//   * within a paragraph, lines are joined and re-broken at whitespace so
//     no line exceeds `width` (vim's `textwidth`)
//   * whitespace at join/break points is normalised to one space (two after
//     `.`/`?`/`!` with `joinspaces`); other interior whitespace is kept
//   * the first line's indentation is carried to every output line and its
//     width (tabs expanded to `tabstop`) counts against `width`
//   * a single word longer than the available width sits alone on its line
//   * code: a run of lines with the same comment leader is one paragraph and
//     the leader is re-applied to every output line; block comments keep
//     their opener, ` * ` inner lines and closer
//   * prose: `- `, `* `, `+ `, `1. `, `1) ` start a list item; following
//     plain lines continue it with a hanging indent; `>` blockquote leaders
//     behave like comment leaders, keyed by nesting depth
//
// Known gaps: a list marker inside a blockquote gets no hanging indent, and
// `*`-banner comments (`/*****`) are reflowed like any other block comment.
//
// Widths are measured in UTF-16 code units (tabs inside content count as 1),
// which is right for ASCII prose and close enough elsewhere.

export const DEFAULT_TEXT_WIDTH = 79;

export interface HardWrapOptions {
  /** Monaco language id. Prose ids (markdown, plaintext, ...) or undefined
   *  get list/blockquote handling; anything else gets comment leaders. */
  languageId?: string;
  /** Columns per tab when measuring indentation (vim `tabstop`, default 8). */
  tabstop?: number;
  /** Two spaces after `.`, `?`, `!` when joining lines (vim `joinspaces`). */
  joinspaces?: boolean;
}

const PROSE_LANGUAGE_IDS = new Set([
  "plaintext",
  "markdown",
  "mdx",
  "restructuredtext",
  "asciidoc",
  "text",
  "org",
]);

interface BlockCommentType {
  start: string;
  inner: string;
  final: string;
}

// Longest-prefix-first within each family so "/**" wins over "/*" and "///"
// over "//".
const BLOCK_COMMENTS: readonly BlockCommentType[] = [
  { start: "/**", inner: "*", final: "*/" },
  { start: "/*", inner: "*", final: "*/" },
  { start: "{-", inner: "-", final: "-}" },
];
const LINE_COMMENTS: readonly string[] = ["///", "//!", "//", "--", "#", ";", "*", "%"];

// Blockquote depth is what identifies the leader (`>` and `> ` are the same
// depth); the emitted form is normalised to `> ` per level.
const QUOTE_RE = /^((?:>[ \t]*)+)(.*)$/;
const LIST_RE = /^([-*+]|\d+[.)])([ \t]+)(\S.*)$/;

type Chunk =
  | { kind: "blank" }
  | { kind: "verbatim"; text: string }
  | {
      kind: "para";
      role: "plain" | "comment" | "quote" | "list";
      // What a following line must match to join this paragraph (see the
      // chunkers for the per-role rule).
      leader: string;
      indent: string;
      // Prefix (after indent) for the first / subsequent output lines.
      first: string;
      rest: string;
      content: string[];
    }
  | {
      kind: "block";
      indent: string;
      type: BlockCommentType;
      content: string[];
      closed: boolean;
      // The closer sat alone on its own line in the input; keep it there.
      closerOwnLine: boolean;
    };

export function hardWrapLines(
  lines: readonly string[],
  width: number,
  opts: HardWrapOptions = {},
): string[] {
  const tabstop = opts.tabstop && opts.tabstop > 0 ? opts.tabstop : 8;
  const joinspaces = opts.joinspaces ?? false;
  const chunks = isProse(opts.languageId) ? chunkProse(lines) : chunkCode(lines);
  const out: string[] = [];
  for (const chunk of chunks) emitChunk(chunk, width, tabstop, joinspaces, out);
  return out;
}

function isProse(languageId: string | undefined): boolean {
  return !languageId || PROSE_LANGUAGE_IDS.has(languageId);
}

function splitIndent(line: string): [indent: string, text: string] {
  const indent = /^[ \t]*/.exec(line)?.[0] ?? "";
  return [indent, line.slice(indent.length)];
}

function displayWidth(s: string, tabstop: number): number {
  let col = 0;
  for (const ch of s) col += ch === "\t" ? tabstop - (col % tabstop) : 1;
  return col;
}

// ---- Chunking: group input lines into paragraphs by leader ----------------

function chunkCode(lines: readonly string[]): Chunk[] {
  const chunks: Chunk[] = [];
  for (const line of lines) {
    if (line.trim() === "") {
      chunks.push({ kind: "blank" });
      continue;
    }
    const [indent, text] = splitIndent(line);
    const last = chunks[chunks.length - 1];

    // Inside an open block comment: closer alone, closer inline, or an
    // inner-leader line. Anything else abandons the block unterminated.
    if (last?.kind === "block" && !last.closed) {
      const { inner, final } = last.type;
      const trimmed = text.trimEnd();
      if (isCloserLine(trimmed, last.type)) {
        last.closed = true;
        last.closerOwnLine = true;
        continue;
      }
      if (trimmed.endsWith(final)) {
        let body = trimmed.slice(0, -final.length);
        if (body.startsWith(inner)) body = body.slice(inner.length);
        last.content.push(body.trim());
        last.closed = true;
        continue;
      }
      if (text.startsWith(inner)) {
        last.content.push(text.slice(inner.length).trim());
        continue;
      }
    }

    // A block comment that opens and closes in one go with nothing inside
    // (`/**/`, `/***/`, `{-}`) is left alone along with anything after it:
    // the opener overlaps the closer and there's nothing to reflow anyway.
    const block = BLOCK_COMMENTS.find((t) => text.startsWith(t.start));
    if (block && BLOCK_COMMENTS.some((t) => isEmptyBlockComment(text, t))) {
      chunks.push({ kind: "verbatim", text: line.trimEnd() });
      continue;
    }
    if (block) {
      let body = text.slice(block.start.length).trimEnd();
      let closed = false;
      if (body.endsWith(block.final)) {
        body = body.slice(0, -block.final.length);
        closed = true;
      }
      chunks.push({
        kind: "block",
        indent,
        type: block,
        content: [body.trim()],
        closed,
        closerOwnLine: false,
      });
      continue;
    }

    // A stray closer (its block was broken by a non-comment line above):
    // leave it exactly as it was rather than treating "*/" as a "*" leader.
    if (BLOCK_COMMENTS.some((t) => isCloserLine(text.trimEnd(), t))) {
      chunks.push({ kind: "verbatim", text: line.trimEnd() });
      continue;
    }

    // Line comment, or plain text when no leader matches (leader "").
    const leader = LINE_COMMENTS.find((l) => text.startsWith(l)) ?? "";
    const after = text.slice(leader.length);
    const spacing = /^[ \t]*/.exec(after)?.[0] ?? "";
    const body = after.trim();
    if (body === "") {
      // A bare leader (`//`, `#`) separates paragraphs within a comment
      // block. Emit it as-is rather than letting it seed a paragraph, so the
      // next real line captures its own leader spacing.
      chunks.push({ kind: "verbatim", text: indent + leader });
    } else if (last?.kind === "para" && last.leader === leader) {
      last.content.push(body);
    } else {
      chunks.push({
        kind: "para",
        role: leader ? "comment" : "plain",
        leader,
        indent,
        first: leader + spacing,
        rest: leader + spacing,
        content: [body],
      });
    }
  }
  return chunks;
}

// `*/`, `**/`, `-}` etc.: a closer on its own line, allowing repeated inner
// characters before it as in the common `**/` javadoc style.
function isCloserLine(trimmed: string, type: BlockCommentType): boolean {
  if (!trimmed.endsWith(type.final)) return false;
  const before = trimmed.slice(0, -type.final.length);
  return before.split("").every((c) => c === type.inner);
}

// `/**/`, `/***/`, `{-}`: the opener runs straight into the closer, sharing
// its first character, with only inner characters between.
function isEmptyBlockComment(text: string, type: BlockCommentType): boolean {
  if (!text.startsWith(type.start)) return false;
  let i = type.start.length;
  while (text.startsWith(type.inner, i)) i += type.inner.length;
  return text.startsWith(type.final.slice(1), i);
}

function chunkProse(lines: readonly string[]): Chunk[] {
  const chunks: Chunk[] = [];
  for (const line of lines) {
    if (line.trim() === "") {
      chunks.push({ kind: "blank" });
      continue;
    }
    const [indent, text] = splitIndent(line);
    const last = chunks[chunks.length - 1];

    const quote = QUOTE_RE.exec(text);
    if (quote) {
      const depth = quote[1].split(">").length - 1;
      const leader = "> ".repeat(depth);
      const body = quote[2].trim();
      if (last?.kind === "para" && last.role === "quote" && last.leader === leader) {
        last.content.push(body);
      } else {
        chunks.push({
          kind: "para",
          role: "quote",
          leader,
          indent,
          first: leader,
          rest: leader,
          content: [body],
        });
      }
      continue;
    }

    // A list marker always starts a new item (vim's `fb:` comment flag).
    const item = LIST_RE.exec(text);
    if (item) {
      const marker = item[1] + item[2];
      chunks.push({
        kind: "para",
        role: "list",
        leader: marker,
        indent,
        first: marker,
        rest: " ".repeat(marker.length),
        content: [item[3].trim()],
      });
      continue;
    }

    // Plain text continues a plain paragraph or a list item (lazy
    // continuation, as in Markdown and vim's `fb:` handling).
    const body = text.trim();
    if (last?.kind === "para" && (last.role === "plain" || last.role === "list")) {
      last.content.push(body);
    } else {
      chunks.push({
        kind: "para",
        role: "plain",
        leader: "",
        indent,
        first: "",
        rest: "",
        content: [body],
      });
    }
  }
  return chunks;
}

// ---- Emission: fill each chunk and re-apply indent + leaders --------------

function emitChunk(
  chunk: Chunk,
  width: number,
  tabstop: number,
  joinspaces: boolean,
  out: string[],
): void {
  switch (chunk.kind) {
    case "blank":
      out.push("");
      return;
    case "verbatim":
      out.push(chunk.text);
      return;
    case "para": {
      const indentWidth = displayWidth(chunk.indent, tabstop);
      const filled = fill(
        chunk.content,
        width - indentWidth - chunk.first.length,
        width - indentWidth - chunk.rest.length,
        joinspaces,
      );
      filled.forEach((text, i) => {
        out.push((chunk.indent + (i === 0 ? chunk.first : chunk.rest) + text).trimEnd());
      });
      return;
    }
    case "block": {
      const { indent, type } = chunk;
      const indentWidth = displayWidth(indent, tabstop);
      // First line is "/* text", the rest " * text".
      const filled = fill(
        chunk.content,
        width - indentWidth - type.start.length - 1,
        width - indentWidth - type.inner.length - 2,
        joinspaces,
      );
      const lines = filled.map((text, i) =>
        i === 0
          ? indent + type.start + (text ? " " + text : "")
          : indent + " " + type.inner + (text ? " " + text : ""),
      );
      if (chunk.closed) {
        const closer = " " + type.final;
        const lastLine = lines[lines.length - 1];
        const lastWidth = indentWidth + lastLine.length - indent.length;
        if (!chunk.closerOwnLine && lastWidth + closer.length <= width) {
          lines[lines.length - 1] = lastLine + closer;
        } else {
          lines.push(indent + closer);
        }
      }
      out.push(...lines);
      return;
    }
  }
}

// Join `content` lines and re-break them so the first output line is at most
// `firstAvail` wide and later ones at most `restAvail`. Empty content lines
// are preserved as paragraph breaks. Interior whitespace is kept; only the
// whitespace at join and break points is normalised.
function fill(
  content: readonly string[],
  firstAvail: number,
  restAvail: number,
  joinspaces: boolean,
): string[] {
  const out: string[] = [];
  let cur = "";
  const avail = () => (out.length === 0 ? firstAvail : restAvail);
  const flush = () => {
    if (cur !== "") {
      out.push(cur);
      cur = "";
    }
  };

  for (const line of content) {
    let rest = line.trim();
    if (rest === "") {
      flush();
      out.push("");
      continue;
    }
    while (rest !== "") {
      const sep = cur === "" ? "" : joinspaces && /[.?!]$/.test(cur) ? "  " : " ";
      const room = avail() - cur.length - sep.length;
      if (rest.length <= room) {
        cur += sep + rest;
        break;
      }
      let bp = lastWhitespaceAtOrBefore(rest, room);
      if (bp < 0) {
        // The next word doesn't fit. Retry on a fresh line if we have one to
        // give up; otherwise it's longer than the line and goes on alone.
        if (cur !== "") {
          flush();
          continue;
        }
        bp = rest.search(/\s/);
        if (bp < 0) bp = rest.length;
      }
      cur += sep + rest.slice(0, bp).trimEnd();
      flush();
      rest = rest.slice(bp).trimStart();
    }
  }
  flush();
  return out;
}

function lastWhitespaceAtOrBefore(s: string, limit: number): number {
  for (let i = Math.min(limit, s.length - 1); i >= 0; i--) {
    if (/\s/.test(s[i])) return i;
  }
  return -1;
}
