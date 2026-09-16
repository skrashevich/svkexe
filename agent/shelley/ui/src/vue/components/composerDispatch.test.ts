import assert from "node:assert/strict";
import {
  composerDispatch,
  composerSessionID,
  guardComposerClear,
  type ComposerSubmissionIntent,
} from "./composerDispatch";

const intents: ComposerSubmissionIntent[] = [
  "send",
  "send-now",
  "queue",
  "compact-and-send",
  "auto-queue",
];
for (const intent of intents) {
  assert.deepEqual(
    composerDispatch(" \t/btw\n what changed?  ", { intent }),
    { route: "btw", question: "what changed?" },
    `/btw bypasses the ${intent} route`,
  );
  assert.deepEqual(
    composerDispatch(" \t/btw\r\n", { intent }),
    { route: "btw", question: "" },
    `bare /btw bypasses the ${intent} route`,
  );
}

const routeCases: Array<
  [string, Parameters<typeof composerDispatch>[1], ReturnType<typeof composerDispatch>["route"]]
> = [
  ["ordinary", { intent: "send" }, "send"],
  ["ordinary", { intent: "send-now" }, "send"],
  ["ordinary", { intent: "queue" }, "queue"],
  ["ordinary", { intent: "auto-queue" }, "queue"],
  ["ordinary", { intent: "compact-and-send" }, "compact-and-send"],
  ["ordinary", {}, "send"],
  ["/clear", {}, "send"],
  ["!git status", {}, "send"],
  ["/btwfoo", {}, "send"],
  ["/btw question", { isChildConversation: true }, "btw-blocked"],
];
for (const [message, options, route] of routeCases) {
  assert.equal(composerDispatch(message, options).route, route, `${message} uses ${route}`);
}

assert.equal(composerSessionID("draft", "draft"), null);
assert.equal(composerSessionID(null, "draft"), null);
assert.notEqual(composerSessionID("parent-a", null), composerSessionID("parent-b", null));

let clears = 0;
for (const [origin, current] of [
  [
    { session: "parent-a", generation: 1 },
    { session: "parent-a", generation: 1 },
  ],
  [
    { session: "parent-a", generation: 1 },
    { session: "parent-a", generation: 2 },
  ],
  [
    { session: "parent-a", generation: 1 },
    { session: "parent-b", generation: 1 },
  ],
  [
    { session: null, generation: 2 },
    { session: composerSessionID("draft", "draft"), generation: 2 },
  ],
] as const) {
  guardComposerClear(
    origin,
    () => current,
    () => clears++,
  );
}
assert.equal(
  clears,
  2,
  "async completion clears only its originating conversation, generation, or promoted draft",
);
