// vimHardWrap.test.ts — unit tests for the gq/gw paragraph reflow used by
// the Monaco vim adapter. Self-executing on import (see scripts/run-tests.mjs).

import { hardWrapLines, type HardWrapOptions } from "./vimHardWrap";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

function eq(actual: string[], expected: string[], name: string): void {
  const a = JSON.stringify(actual);
  const e = JSON.stringify(expected);
  assert(a === e, `${name}\n  expected: ${e}\n  actual:   ${a}`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

// Most tests exercise a code language so comment leaders are active; prose
// tests opt in explicitly.
const code = (lines: string[], width: number, opts: HardWrapOptions = {}) =>
  hardWrapLines(lines, width, { languageId: "go", ...opts });
const prose = (lines: string[], width: number, opts: HardWrapOptions = {}) =>
  hardWrapLines(lines, width, { languageId: "markdown", ...opts });

// ---- Plain paragraphs ------------------------------------------------------

run("joins short lines into one line under the limit", () => {
  eq(code(["hello", "world"], 79), ["hello world"], "join");
});

run("wraps a long line at word boundaries", () => {
  eq(
    code(["the quick brown fox jumps over the lazy dog"], 20),
    ["the quick brown fox", "jumps over the lazy", "dog"],
    "wrap",
  );
});

run("reflows joined lines to the width", () => {
  eq(code(["aaa bbb", "ccc ddd eee", "fff"], 11), ["aaa bbb ccc", "ddd eee fff"], "reflow");
});

run("a word longer than the width sits on its own line", () => {
  eq(code(["a supercalifragilistic b"], 10), ["a", "supercalifragilistic", "b"], "long word");
  eq(code(["supercalifragilistic"], 10), ["supercalifragilistic"], "lone long word");
  eq(
    code(["supercalifragilistic expialidocious"], 10),
    ["supercalifragilistic", "expialidocious"],
    "two long words",
  );
});

run("blank lines separate paragraphs and are preserved", () => {
  eq(
    code(["one two", "three", "", "four five", "six"], 79),
    ["one two three", "", "four five six"],
    "paragraphs",
  );
});

run("whitespace-only lines count as blank", () => {
  eq(code(["a", "   ", "b"], 79), ["a", "", "b"], "ws blank");
});

run("multiple blank lines are preserved", () => {
  eq(code(["a", "", "", "b"], 79), ["a", "", "", "b"], "multi blank");
});

run("leading indentation of the first line is kept on every output line", () => {
  eq(code(["    aaa bbb ccc ddd"], 12), ["    aaa bbb", "    ccc ddd"], "indent");
});

run("each paragraph uses its own first-line indent", () => {
  eq(
    code(["aaa bbb", "ccc", "", "    ddd eee", "    fff"], 79),
    ["aaa bbb ccc", "", "    ddd eee fff"],
    "per-paragraph indent",
  );
});

run("interior whitespace within a source line is preserved (as vim does)", () => {
  eq(code(["a    b\tc"], 79), ["a    b\tc"], "interior ws");
});

run("leading/trailing whitespace of joined lines is dropped", () => {
  eq(code(["abc   ", "   def"], 79), ["abc def"], "edge ws");
});

run("empty input yields empty output", () => {
  eq(code([], 79), [], "empty");
});

run("a line exactly at the width is not wrapped", () => {
  eq(code(["abcd efgh"], 9), ["abcd efgh"], "exact width");
});

run("a line one over the width wraps", () => {
  eq(code(["abcd efghi"], 9), ["abcd", "efghi"], "one over");
});

run("indentation is measured with tabs expanded to tabstop", () => {
  // One tab at tabstop=4 occupies 4 columns, leaving 8 for content at width 12.
  eq(code(["\taaa bbb ccc"], 12, { tabstop: 4 }), ["\taaa bbb", "\tccc"], "tab indent");
  // At tabstop=8 only 4 columns remain.
  eq(code(["\taaa bbb"], 12, { tabstop: 8 }), ["\taaa", "\tbbb"], "tab indent 8");
});

// ---- Comment leaders (approach adapted from VSCodeVim) ----------------------

run("// comment leader is re-applied to every wrapped line", () => {
  eq(
    code(["  // this is a fairly long comment line"], 22),
    ["  // this is a fairly", "  // long comment line"],
    "// leader",
  );
});

run("# comment lines join and rewrap keeping the leader", () => {
  eq(
    code(["# alpha bravo", "# charlie delta echo", "# foxtrot"], 22),
    ["# alpha bravo charlie", "# delta echo foxtrot"],
    "# leader",
  );
});

run("doc-comment leaders /// and //! are distinct from //", () => {
  eq(code(["/// aaa", "/// bbb"], 79), ["/// aaa bbb"], "///");
  eq(code(["//! aaa", "//! bbb"], 79), ["//! aaa bbb"], "//!");
  eq(code(["/// aaa", "// bbb"], 79), ["/// aaa", "// bbb"], "/// vs //");
});

run("adjacent lines with different leaders are not merged", () => {
  eq(code(["# one", "// two", "three"], 79), ["# one", "// two", "three"], "mixed leaders");
});

run("comment leader width counts against textwidth", () => {
  // "// " is 3 cols, so only 9 cols remain for content at width 12.
  eq(code(["// aaa bbb ccc"], 12), ["// aaa bbb", "// ccc"], "leader width");
});

run("spacing between leader and text is preserved", () => {
  eq(code(["//   aaa", "//   bbb"], 79), ["//   aaa bbb"], "leader spacing");
  eq(code(["//aaa", "//bbb"], 79), ["//aaa bbb"], "no spacing");
});

run("blank comment lines split paragraphs within a comment block", () => {
  eq(
    code(["// aaa", "// bbb", "//", "// ccc"], 79),
    ["// aaa bbb", "//", "// ccc"],
    "blank comment line",
  );
});

run("a bare leader on the first line does not eat the spacing of the rest", () => {
  eq(code(["//", "// a", "// b"], 79), ["//", "// a b"], "// first");
  eq(code(["#", "# a"], 79), ["#", "# a"], "# first");
  eq(code(["//  ", "// a"], 79), ["//", "// a"], "trailing ws on bare leader");
  eq(code(["  //", "  // a"], 79), ["  //", "  // a"], "indented bare leader");
});

run("width smaller than the leader still makes progress", () => {
  eq(code(["// abc def"], 2), ["// abc", "// def"], "tiny width");
  eq(code(["// abc def"], 0), ["// abc", "// def"], "zero width");
});

run("a non-positive tabstop falls back to 8", () => {
  eq(code(["\taaa bbb"], 12, { tabstop: 0 }), ["\taaa", "\tbbb"], "tabstop 0");
});

run("-- ; % and * leaders", () => {
  eq(code(["-- a", "-- b"], 79), ["-- a b"], "--");
  eq(code(["; a", "; b"], 79), ["; a b"], ";");
  eq(code(["% a", "% b"], 79), ["% a b"], "%");
  eq(code([" * a", " * b"], 79), [" * a b"], "* (orphan block middle)");
});

run("/* */ block comment reflows with * inner lines", () => {
  // Closer on its own line stays on its own line.
  eq(
    code(["/* alpha bravo charlie", " * delta echo", " */"], 79),
    ["/* alpha bravo charlie delta echo", " */"],
    "block joins",
  );
  // Inline closer stays inline when it fits...
  eq(code(["/* alpha", " * bravo */"], 79), ["/* alpha bravo */"], "inline closer");
  // ...and moves to its own line when it would overflow.
  eq(
    code(["/* alpha bravo charlie delta echo foxtrot */"], 22),
    ["/* alpha bravo charlie", " * delta echo foxtrot", " */"],
    "block splits",
  );
});

run("/** javadoc block keeps its opener and indentation", () => {
  eq(
    code(["  /** Returns the thing.", "   * Really.", "   */"], 79),
    ["  /** Returns the thing. Really.", "   */"],
    "javadoc",
  );
});

run("{- -} haskell block comment", () => {
  eq(code(["{- a", " - b -}"], 79), ["{- a b -}"], "haskell");
});

run("a block comment left open in the selection gets no spurious closer", () => {
  // VSCodeVim appends " */" unconditionally here; we don't invent one.
  eq(code(["/* alpha", " * bravo"], 79), ["/* alpha bravo"], "unterminated");
});

run("**/ style closer is recognised as a closer line", () => {
  eq(code(["/** a", " * b", " **/"], 79), ["/** a b", " */"], "**/ own line");
});

run("empty block comments are left untouched", () => {
  eq(code(["/**/"], 79), ["/**/"], "/**/");
  eq(code(["/*/"], 79), ["/*/"], "/*/");
  eq(code(["/***/"], 79), ["/***/"], "/***/");
  eq(code(["{-}"], 79), ["{-}"], "{-}");
  eq(code(["/**/ int y;"], 79), ["/**/ int y;"], "code after empty comment");
  eq(code(["  /**/", "int x;"], 79), ["  /**/", "int x;"], "indented + following");
  // A closed-but-not-empty one-liner still reflows normally.
  eq(code(["/* */"], 79), ["/* */"], "/* */");
});

run("a stray closer whose block was broken above is left verbatim", () => {
  eq(code([" * a", " */"], 79), [" * a", " */"], "stray closer");
  eq(code(["x", "*/", "y"], 79), ["x", "*/", "y"], "closer between plain lines");
});

run("block comment body lines without * are left alone, like vim", () => {
  eq(code(["/* alpha", "bravo", "*/"], 79), ["/* alpha", "bravo", "*/"], "no-star body");
});

run("comment leaders are not recognised in prose languages", () => {
  eq(prose(["// a", "// b"], 79), ["// a // b"], "prose //");
  eq(prose(["# Title", "text"], 79), ["# Title text"], "prose #");
});

// ---- Prose: list items and blockquotes ------------------------------------

run("markdown bullets are not merged into each other", () => {
  eq(prose(["* one", "* two"], 79), ["* one", "* two"], "* bullets");
  eq(prose(["- one", "- two"], 79), ["- one", "- two"], "- bullets");
  eq(prose(["+ one", "+ two"], 79), ["+ one", "+ two"], "+ bullets");
  eq(prose(["1. one", "2. two"], 79), ["1. one", "2. two"], "numbered");
  eq(prose(["1) one", "2) two"], 79), ["1) one", "2) two"], "numbered )");
});

run("a bullet's continuation lines join into it with a hanging indent", () => {
  eq(
    prose(["- alpha bravo charlie delta", "  echo foxtrot golf"], 20),
    ["- alpha bravo", "  charlie delta echo", "  foxtrot golf"],
    "hanging indent",
  );
  eq(
    prose(["10. alpha bravo charlie", "    delta"], 16),
    ["10. alpha bravo", "    charlie", "    delta"],
    "wide marker",
  );
});

run("bullet marker without trailing space is ordinary text", () => {
  eq(prose(["*emphasis* and", "-dash"], 79), ["*emphasis* and -dash"], "not bullets");
});

run("indented bullets keep their indentation", () => {
  eq(
    prose(["  - alpha bravo charlie", "    delta"], 16),
    ["  - alpha bravo", "    charlie", "    delta"],
    "indented bullet",
  );
});

run("blockquote > leaders join and re-apply, including nesting", () => {
  eq(prose(["> alpha", "> bravo"], 79), ["> alpha bravo"], "quote");
  eq(prose(["> > alpha", "> > bravo"], 79), ["> > alpha bravo"], "nested quote");
  eq(prose(["> alpha", "> > bravo"], 79), ["> alpha", "> > bravo"], "different depth");
  // Depth, not exact spacing, identifies the leader.
  eq(prose(["> a", ">b"], 79), ["> a b"], "> vs >");
  eq(prose([">> a", "> > b"], 79), ["> > a b"], ">> vs > >");
});

run("plaintext and unknown languages use the prose rules", () => {
  eq(hardWrapLines(["- a", "- b"], 79, { languageId: "plaintext" }), ["- a", "- b"], "plaintext");
  eq(hardWrapLines(["- a", "- b"], 79, {}), ["- a", "- b"], "no language");
});

// ---- joinspaces ------------------------------------------------------------

run("joinspaces puts two spaces after a sentence end when joining lines", () => {
  eq(
    prose(["End of one.", "Start of two.", "Really?", "Yes!", "ok"], 79, { joinspaces: true }),
    ["End of one.  Start of two.  Really?  Yes!  ok"],
    "joinspaces on",
  );
});

run("joinspaces defaults off: single space after sentence end", () => {
  eq(prose(["End of one.", "Start of two."], 79), ["End of one. Start of two."], "default");
});

run("joinspaces only affects joins, not existing spacing within a line", () => {
  eq(prose(["a. b"], 79, { joinspaces: true }), ["a. b"], "existing spacing kept");
});

run("joinspaces separator counts toward the width", () => {
  // "abc." + "  " + "defg" = 10 > 9, so it wraps.
  eq(prose(["abc.", "defg"], 9, { joinspaces: true }), ["abc.", "defg"], "js width");
  eq(prose(["abc.", "defg"], 10, { joinspaces: true }), ["abc.  defg"], "js fits");
});
