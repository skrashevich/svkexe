import assert from "node:assert/strict";
import type { Message } from "../../types";
import { exchange } from "../../test/btwFixtures";
import type { CoalescedItem } from "./coalesce";
import {
  btwAnchor,
  btwExchangesByAnchor,
  btwGenerationStartAnchorKey,
  focusBtwFollowUp,
  latestBtwExchange,
  scrollToBtwExchange,
} from "./btwAnchors";

function item(messageID: string, generation: number, sequenceID: number): CoalescedItem {
  return {
    type: "message",
    generation,
    sourceSequenceID: sequenceID,
    anchorKey: `message:${messageID}`,
    message: { message_id: messageID, generation, sequence_id: sequenceID } as Message,
  };
}

const messageOne = item("m1", 1, 1);
const messageTwo = item("m2", 1, 2);
const tool: CoalescedItem = {
  type: "tool",
  generation: 1,
  sourceSequenceID: 3,
  anchorKey: "tool:t1",
  toolUseId: "t1",
};
const items = [messageOne, messageTwo, tool];
for (const [name, pointer, expected] of [
  ["sequence placement", { generation: 1, sequence_id: 2 }, "message:m2"],
  ["tool boundary", { generation: 1, sequence_id: 3 }, "tool:t1"],
  ["hidden generation", { generation: 2, sequence_id: 99 }, btwGenerationStartAnchorKey(2)],
] as const) {
  assert.equal(btwAnchor(pointer, items).key, expected, name);
}

const first = exchange("first", 1, 1, "2026-08-31T00:01:00Z");
const second = exchange("second", 1, 2, "2026-08-31T00:02:00Z");
const third = exchange("third", 1, 2, "2026-08-31T00:03:00Z");
const hidden = exchange("hidden", 2, 4, "2026-08-31T00:04:00Z");
const byAnchor = btwExchangesByAnchor([third, hidden, second, first], items);
assert.deepEqual(
  [...byAnchor].map(([anchor, exchanges]) => [anchor, exchanges.map((value) => value.exchange_id)]),
  [
    ["message:m2", ["second", "third"]],
    [btwGenerationStartAnchorKey(2), ["hidden"]],
    ["message:m1", ["first"]],
  ],
  "exchanges group by pointer and sort oldest-first within an anchor",
);

let scrolledTo = "";
const target = (id: string) =>
  ({
    dataset: { btwExchangeId: id },
    scrollIntoView: () => {
      scrolledTo = id;
    },
  }) as unknown as HTMLElement;
const root = {
  querySelectorAll: () => [target("first"), target("third")],
} as unknown as ParentNode;
assert.equal(latestBtwExchange([third, first]), third, "the newest-first exchange is latest");
assert(scrollToBtwExchange(third.exchange_id, root));
assert.equal(scrolledTo, "third");

let focused = false;
let expanded = false;
const followUpExchange = {
  dataset: { btwExchangeId: third.exchange_id },
  querySelector: (selector: string) =>
    selector === ".btw-inline-label"
      ? {
          ariaExpanded: "false",
          click: () => {
            expanded = true;
          },
        }
      : {
          disabled: false,
          focus: () => {
            focused = true;
          },
        },
};
const followUpRoot = {
  querySelectorAll: () => [followUpExchange],
} as unknown as ParentNode;
assert(await focusBtwFollowUp(third.exchange_id, followUpRoot));
assert(expanded && focused, "the latest collapsed follow-up expands and focuses");
