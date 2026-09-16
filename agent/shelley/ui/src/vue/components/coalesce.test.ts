// Unit tests for coalesceMessages (ChatInterface message/tool splitting).
// Run with: tsx src/vue/components/coalesce.test.ts
import { coalesceMessages } from "./coalesce";
import type { Message, LLMContent, LLMMessage } from "../../types";

let passed = 0;
let failed = 0;
const failures: string[] = [];
function check(name: string, cond: boolean, detail?: unknown) {
  if (cond) {
    passed++;
  } else {
    failed++;
    failures.push(`✗ ${name}${detail !== undefined ? `\n   ${JSON.stringify(detail)}` : ""}`);
  }
}

let seq = 0;
function agentMessage(content: LLMContent[]): Message {
  seq++;
  const llm: LLMMessage = { Role: 1, Content: content };
  return {
    message_id: `m${seq}`,
    conversation_id: "c1",
    sequence_id: seq,
    type: "agent",
    generation: 1,
    llm_data: JSON.stringify(llm),
  } as unknown as Message;
}
function text(t: string): LLMContent {
  return { ID: "", Type: 2, Text: t } as LLMContent;
}
function thinking(t: string): LLMContent {
  return { ID: "", Type: 3, Text: t } as LLMContent;
}
function toolUse(id: string): LLMContent {
  return { ID: id, Type: 5, ToolName: "bash", ToolInput: { command: "true" } } as LLMContent;
}
function serverToolUse(id: string): LLMContent {
  return { ID: id, Type: 7, ToolName: "web_search", ToolInput: {} } as LLMContent;
}

// --- Text + tool use: one message item and one tool item ---
{
  const items = coalesceMessages([agentMessage([text("hello"), toolUse("t1")])]);
  check(
    "text+tool -> message and tool items",
    items.length === 2 && items[0].type === "message" && items[1].type === "tool",
    items,
  );
}

// --- Thinking-only turn with a tool call still renders the thinking ---
{
  const items = coalesceMessages([agentMessage([thinking("chain of thought"), toolUse("t2")])]);
  check(
    "thinking+tool -> message and tool items",
    items.length === 2 && items[0].type === "message" && items[1].type === "tool",
    items,
  );
}

// --- Thinking-only turn (no text, no tools) renders the thinking ---
{
  const items = coalesceMessages([agentMessage([thinking("just pondering")])]);
  check("thinking only -> message item", items.length === 1 && items[0].type === "message", items);
}

// --- Empty thinking (e.g. signature-only block) is not renderable ---
{
  const items = coalesceMessages([agentMessage([thinking(""), toolUse("t3")])]);
  check(
    "empty thinking+tool -> tool item only",
    items.length === 1 && items[0].type === "tool",
    items,
  );
}

// --- Tool-only turn produces no message item ---
{
  const items = coalesceMessages([agentMessage([toolUse("t4")])]);
  check("tool only -> tool item only", items.length === 1 && items[0].type === "tool", items);
}

// --- Text written after the tool calls renders after them ---
{
  // A provider running server-side web_search returns ONE assistant message
  // whose blocks interleave thinking, searches, and the final answer. The
  // answer is authored last, so rendering it above the searches makes the
  // agent look like it answered before doing the research.
  const items = coalesceMessages([
    agentMessage([
      thinking("planning searches"),
      serverToolUse("ws1"),
      thinking("more"),
      serverToolUse("ws2"),
      text("## Findings"),
    ]),
  ]);
  check(
    "trailing text -> tools first, message last",
    items.length === 3 &&
      items[0].type === "tool" &&
      items[1].type === "tool" &&
      items[2].type === "message",
    items.map((i) => i.type),
  );
}

// --- Preamble text still renders before the tool it introduces ---
{
  const items = coalesceMessages([agentMessage([text("I'll check"), toolUse("t5")])]);
  check(
    "leading text -> message first, tool last",
    items.length === 2 && items[0].type === "message" && items[1].type === "tool",
    items.map((i) => i.type),
  );
}

// --- Text on both sides keeps the historical (text-first) placement ---
{
  const items = coalesceMessages([
    agentMessage([text("first I'll look"), toolUse("t6"), text("and here's what I found")]),
  ]);
  check(
    "text before and after -> message first",
    items.length === 2 && items[0].type === "message" && items[1].type === "tool",
    items.map((i) => i.type),
  );
}

// --- Thinking-only turns are unaffected by the trailing-text rule ---
{
  const items = coalesceMessages([agentMessage([thinking("pondering"), toolUse("t7")])]);
  check(
    "thinking + tool -> message first",
    items.length === 2 && items[0].type === "message" && items[1].type === "tool",
    items.map((i) => i.type),
  );
}

// --- Slug markers are dropped entirely ---
{
  // A slug marker records the cost of the LLM call that named the conversation.
  // It has no content and renders as nothing, but a coalesced item is what
  // drives timestamp/day-separator emission, so leaving one in prints a stray
  // date header with nothing underneath (observed as a spurious "Thu, Jul 30"
  // band in the real UI). Its cost is read straight off messages elsewhere.
  const marker = {
    message_id: "slug1",
    conversation_id: "c1",
    sequence_id: 3,
    type: "slug",
    generation: 1,
    llm_data: null,
    other_usage_data: '[{"purpose":"slug","input_tokens":92}]',
    created_at: "2026-07-30T14:36:00Z",
  } as unknown as Message;
  check(
    "lone slug marker -> no items",
    coalesceMessages([marker]).length === 0,
    coalesceMessages([marker]),
  );

  const withNeighbors = coalesceMessages([
    agentMessage([text("before")]),
    marker,
    agentMessage([text("after")]),
  ]);
  check(
    "slug marker between messages -> only the two real messages",
    withNeighbors.length === 2 && withNeighbors.every((i) => i.type === "message"),
    withNeighbors,
  );
  // The visible bug was chrome, not the item: buildRenderModel emits a
  // timestamp (and a day-separator when the day rolls over) per coalesced item,
  // and advances its "last seen minute/day" state. Since buildRenderModel lives
  // inside ChatInterface.vue and isn't importable, assert the chokepoint that
  // feeds it: no coalesced item for a marker means no chrome for a marker, and
  // no marker timestamp can suppress the next real message's.
  check(
    "no slug marker survives coalescing, so it cannot emit timestamp/day-separator chrome",
    !withNeighbors.some((i) => i.message?.type === "slug"),
    withNeighbors,
  );
}

console.log(`\ncoalesceMessages Tests: ${passed} passed, ${failed} failed\n`);
if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("All tests passed!");
process.exit(0);
