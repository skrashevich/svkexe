import type { GitTour } from "../../services/api";
import { buildTourContents } from "./commitTourContents";

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(
      `${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`,
    );
  }
}

function run(name: string, fn: () => void): void {
  fn();
  console.log(`✓ ${name}`);
}

const patch = (start: number) =>
  [
    "diff --git a/src/example.ts b/src/example.ts",
    "index 1111111..2222222 100644",
    "--- a/src/example.ts",
    "+++ b/src/example.ts",
    `@@ -${start},1 +${start},1 @@`,
    "-old",
    "+new",
  ].join("\n");

run("tour contents follows narrative order with unique anchors", () => {
  const tour: GitTour = {
    version: 1,
    title: "Example tour",
    intro: "Start here.",
    chunks: [
      { header: "## Core behavior" },
      { patch: patch(2), comment: "First change." },
      { patch: patch(18), comment: "Second change." },
    ],
  };

  assertEqual(
    buildTourContents(tour, true),
    [
      { anchor: "tour-overview", label: "Overview", kind: "overview" },
      { anchor: "tour-entry-0", label: "Core behavior", kind: "section" },
      {
        anchor: "tour-entry-1",
        label: "src/example.ts · lines 2–2",
        kind: "change",
        nested: true,
      },
      {
        anchor: "tour-entry-2",
        label: "src/example.ts · lines 18–18",
        kind: "change",
        nested: true,
      },
    ],
    "contents",
  );
});

run("a tour without overview content starts at its first change", () => {
  const tour: GitTour = { version: 1, chunks: [{ patch: patch(7), trivial: true }] };
  assertEqual(
    buildTourContents(tour, false),
    [
      {
        anchor: "tour-entry-0",
        label: "src/example.ts · lines 7–7",
        kind: "change",
        nested: false,
      },
    ],
    "contents",
  );
});
