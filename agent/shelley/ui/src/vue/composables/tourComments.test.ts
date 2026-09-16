// Self-executing regression tests for commit-tour line comments.

import { buildTourCommentBlock, patchLineText } from "./tourComments";

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  if (actual !== expected) {
    throw new Error(
      `${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`,
    );
  }
}

function run(name: string, fn: () => void): void {
  fn();
  console.log(`✓ ${name}`);
}

const patch = [
  "diff --git a/example.ts b/example.ts",
  "index 1111111..2222222 100644",
  "--- a/example.ts",
  "+++ b/example.ts",
  "@@ -10,3 +20,3 @@ function first() {",
  " shared first",
  "-old first",
  "+new first",
  " tail first",
  "@@ -40,3 +50,4 @@ function second() {",
  " shared second",
  "-old second",
  "+new second",
  "+extra second",
  " tail second",
].join("\n");

run("patchLineText maps both sides across every hunk", () => {
  assertEqual(patchLineText(patch, "deletions", 10), "shared first", "first old context");
  assertEqual(patchLineText(patch, "additions", 21), "new first", "first addition");
  assertEqual(patchLineText(patch, "deletions", 41), "old second", "second deletion");
  assertEqual(patchLineText(patch, "additions", 52), "extra second", "second addition");
  assertEqual(patchLineText(patch, "additions", 53), "tail second", "second new context");
});

run("patchLineText returns null for a line absent from the requested side", () => {
  assertEqual(patchLineText(patch, "additions", 40), null, "old-only coordinate");
  assertEqual(patchLineText(patch, "deletions", 42), "tail second", "old side advances once");
  assertEqual(patchLineText(patch, "deletions", 99), null, "outside every hunk");
});

run("buildTourCommentBlock matches Monaco comment formatting", () => {
  assertEqual(
    buildTourCommentBlock(
      {
        where: "src/example.ts line 21 (new)",
        reference: "src/example.ts:21",
        selectedText: "  new value  ",
        quoteCode: true,
      },
      "Looks good",
    ),
    "> src/example.ts:21: new value\nLooks good\n\n",
    "selected line comment",
  );
  assertEqual(
    buildTourCommentBlock(
      {
        where: "src/example.ts line 23 (new)",
        reference: "src/example.ts:23",
        selectedText: "",
        quoteCode: true,
      },
      "Why blank?",
    ),
    "> src/example.ts:23: \nWhy blank?\n\n",
    "empty line comment",
  );
});
