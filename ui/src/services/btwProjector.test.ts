import assert from "node:assert/strict";
import type { BtwTurn, Message } from "../types";
import {
  agent,
  childConversation,
  idleTransient,
  message,
  readerDescriptor,
  text,
  user,
} from "../test/btwFixtures";
import type { TransientState } from "./messageStore";
import { BTW_INTERRUPTED_ERROR, projectBtwReader } from "./btwProjector";

const descriptor = readerDescriptor();
const conversation = childConversation();
const project = (messages: Message[], transient: TransientState = idleTransient) =>
  projectBtwReader(descriptor, { conversation, messages }, transient);
const turnView = ({ id, question, answer, status, kind }: BtwTurn) => ({
  id,
  question,
  answer,
  status,
  kind,
});

const turns = project([
  message(1, "system", [text("hidden")]),
  user(2, "first"),
  agent(3, "one"),
  user(4, "summary request", { userData: { btw_turn_kind: "summary" } }),
  user(5, "follow up"),
  agent(6, "summary answer"),
  agent(7, "follow-up answer"),
]);
assert.deepEqual(
  turns.turns.map(turnView),
  [
    {
      id: "m2",
      question: "first",
      answer: "one",
      status: "completed",
      kind: "question",
    },
    {
      id: "m4",
      question: "Summary for main chat",
      answer: "summary answer",
      status: "completed",
      kind: "summary",
    },
    {
      id: "m5",
      question: "follow up",
      answer: "follow-up answer",
      status: "completed",
      kind: "question",
    },
  ],
  "initial, summary, and queued follow-up turns stay distinct",
);
assert.equal(turns.status, "completed");

const streaming = project(
  [user(1, "first"), user(2, "queued"), agent(3, "persisted ", { endOfTurn: false })],
  { ...idleTransient, agentWorking: true, streamingText: "live" },
);
assert.deepEqual(
  streaming.turns.map(({ answer, status }) => ({ answer, status })),
  [
    { answer: "persisted live", status: "active" },
    { answer: "", status: "pending" },
  ],
  "persisted and transient output stays on the active serialized turn",
);

const tools = project(
  [
    user(1, "tools"),
    message(2, "agent", [
      { ID: "bash-id", Type: 5, ToolName: "bash", ToolInput: { command: "pwd" } },
      { ID: "search-id", Type: 5, ToolName: "keyword_search", ToolInput: {} },
    ]),
    message(3, "user", [{ ID: "", Type: 6, ToolUseID: "bash-id", ToolResult: [text("/repo")] }]),
    message(4, "user", [{ ID: "", Type: 8, ToolUseID: "search-id" }]),
  ],
  { ...idleTransient, agentWorking: true },
);
assert.deepEqual(
  {
    turns: tools.turns.length,
    count: tools.turns[0].tool_call_count,
    unresolved: tools.turns[0].unresolved_tool_call_count,
    calls: tools.turns[0].tool_calls,
  },
  {
    turns: 1,
    count: 2,
    unresolved: 0,
    calls: [{ name: "bash", command: "pwd" }, { name: "keyword_search" }],
  },
  "tool results resolve projected calls without becoming turns",
);

const failed = project([
  user(1, "fail"),
  message(2, "error", "network failed", {
    userData: { retryable: true },
    endOfTurn: true,
  }),
]);
assert.deepEqual(
  [failed.status, failed.retryable, failed.turns[0].error],
  ["failed", true, "network failed"],
);

const cancelled = project([user(1, "cancel"), agent(2, "[Operation cancelled]")]);
assert.deepEqual(
  [cancelled.status, cancelled.turns[0].status, cancelled.turns[0].answer],
  ["cancelled", "cancelled", ""],
);

const interrupted = project([user(1, "resume me")]);
assert.deepEqual(
  [interrupted.status, interrupted.retryable, interrupted.turns[0].error],
  ["failed", true, BTW_INTERRUPTED_ERROR],
  "a persisted idle row projects an unfinished turn as interrupted",
);

const persistedWorking = projectBtwReader(
  descriptor,
  {
    conversation: childConversation("child", "2026-08-31T12:00:00Z", true),
    messages: [user(1, "still running")],
  },
  idleTransient,
);
assert.deepEqual(
  [persistedWorking.status, persistedWorking.retryable, persistedWorking.turns[0].error],
  ["active", false, null],
  "a persisted working row stays active before transient state catches up",
);

for (const [name, staleConversation, staleTransient] of [
  ["conversation row", childConversation("child", "2026-08-31T12:00:00Z", true), idleTransient],
  ["transient flag", conversation, { ...idleTransient, agentWorking: true }],
] as const) {
  const completedWithStaleWorking = projectBtwReader(
    descriptor,
    {
      conversation: staleConversation,
      messages: [user(1, "already done"), agent(2, "final answer")],
    },
    staleTransient,
  );
  assert.deepEqual(
    [
      completedWithStaleWorking.status,
      completedWithStaleWorking.retryable,
      completedWithStaleWorking.turns[0].status,
      completedWithStaleWorking.turns[0].answer,
    ],
    ["completed", false, "completed", "final answer"],
    `a persisted end-of-turn message wins over a stale working ${name}`,
  );
}

const failedAttempt = [
  user(1, "retry"),
  agent(2, "partial", { endOfTurn: false }),
  message(3, "error", "failed", { userData: { retryable: true }, endOfTurn: true }),
];
const retrying = project(failedAttempt, {
  ...idleTransient,
  agentWorking: true,
  streamingText: "retry stream",
});
const retryDone = project([...failedAttempt, agent(4, "final")]);
assert.deepEqual(
  [
    [retrying.status, retrying.turns[0].answer],
    [retryDone.status, retryDone.turns[0].answer],
  ],
  [
    ["active", "retry stream"],
    ["completed", "final"],
  ],
  "transient and persisted retries replace the failed attempt",
);

const reasked = project([user(1, "ask again"), user(2, "ask again"), agent(3, "new answer")]);
assert.deepEqual(
  reasked.turns.map(({ status, answer }) => ({ status, answer })),
  [
    { status: "failed", answer: "" },
    { status: "completed", answer: "new answer" },
  ],
  "re-asking creates a replacement turn",
);

const purityInput = [user(1, "pure"), agent(2, "answer")];
const before = JSON.stringify({ descriptor, conversation, purityInput, idleTransient });
const first = project(purityInput);
const second = project(purityInput);
assert.deepEqual(first, second, "projection is deterministic");
assert.equal(
  JSON.stringify({ descriptor, conversation, purityInput, idleTransient }),
  before,
  "projection does not mutate inputs",
);

console.log("✓ BTW projector derives turns, tools, terminal states, retries, and streaming");
