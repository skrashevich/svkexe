import assert from "node:assert/strict";
import type { BtwToolCall } from "../types";
import { btwToolCallTooltip } from "./btwToolTally";

const cases: Array<[BtwToolCall[] | undefined, string]> = [
  [
    [{ name: "bash" }, { name: "keyword_search" }, { name: "bash" }, { name: "legacy" }],
    "• bash ×2\n• keyword_search\n• legacy",
  ],
  [
    [
      { name: "bash", command: "  rg -n 'ToolInput' server/\nui/src/  " },
      { name: "keyword_search" },
      { name: "keyword_search" },
      { name: "bash", command: " \n " },
    ],
    "• bash — rg ToolInput\n• keyword_search ×2\n• bash",
  ],
  [
    [{ name: "bash", command: "cd /repo && go test ./server -run TestBTW" }],
    "• bash — go test ./server",
  ],
  [[{ name: "bash", command: "git status && git diff" }], "• bash — git status"],
  [undefined, ""],
];

for (const [calls, expected] of cases) assert.equal(btwToolCallTooltip(calls), expected);

console.log("btwToolTally tests passed");
