import type { ConversationWithState } from "../types";
import {
  compareParticipantGroupKeys,
  filterConversationsByParticipantQuery,
  hasMultiParticipantConversation,
  hasOtherParticipant,
  participantGroupKey,
  participantGroupLabel,
} from "./conversationParticipantFilter";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

function participant(email: string, message_count = 1) {
  return { email, message_count };
}

function conversation(
  id: string,
  participants?: { email: string; message_count: number }[] | null,
): ConversationWithState {
  return { conversation_id: id, participants } as ConversationWithState;
}

const current = conversation("current", [participant("me@example.com", 3)]);
const shared = conversation("shared", [
  participant("other@example.com", 2),
  participant("ME@example.com"),
]);
const other = conversation("other", [participant("other@example.com", 4)]);
const unattributed = conversation("unattributed");
const empty = conversation("empty", []);
const subagent = conversation("subagent", [participant("third@example.com")]);
subagent.parent_conversation_id = "current";
const draft = conversation("draft", []);
draft.is_draft = true;
const corpus = [current, shared, other, unattributed, empty, subagent, draft];

assert(hasOtherParticipant(corpus, "me@example.com"), "detects another known participant");
assert(
  !hasOtherParticipant([current, unattributed, empty], "me@example.com"),
  "single-user and unattributed lists stay single-user",
);
assert(!hasOtherParticipant(corpus, ""), "an unauthenticated request cannot enable the filter");

const filtered = filterConversationsByParticipantQuery(corpus, ["me@example.com"], false);
assert(
  filtered.map((item) => item.conversation_id).join(",") === "current,shared,subagent,draft",
  "matches a selected user without filtering subagents or drafts",
);
assert(
  filterConversationsByParticipantQuery(corpus, ["OTHER@example.com"], false)
    .map((item) => item.conversation_id)
    .join(",") === "shared,other,subagent,draft",
  "matches selected users case-insensitively",
);
assert(
  filterConversationsByParticipantQuery(corpus, ["me@example.com", "other@example.com"], false)
    .map((item) => item.conversation_id)
    .join(",") === "shared,subagent,draft",
  "multiple users use AND semantics",
);
assert(
  filterConversationsByParticipantQuery(corpus, [], true)
    .map((item) => item.conversation_id)
    .join(",") === "unattributed,empty,subagent,draft",
  "unattributed can stand alone",
);
assert(
  filterConversationsByParticipantQuery(corpus, ["me@example.com"], true)
    .map((item) => item.conversation_id)
    .join(",") === "subagent,draft",
  "unattributed is mutually exclusive with user constraints",
);
assert(
  filterConversationsByParticipantQuery(corpus, [], false) === corpus,
  "an empty participant facet is a no-op",
);

assert(hasMultiParticipantConversation(corpus), "detects a multi-participant conversation");
assert(
  !hasMultiParticipantConversation([current, other, unattributed, empty]),
  "disjoint single-user conversations are not multi-participant",
);

const sharedReversed = conversation("shared-reversed", [
  participant("me@example.com"),
  participant("Other@example.com"),
  participant("other@example.com"),
]);
assert(
  participantGroupKey(shared) === participantGroupKey(sharedReversed),
  "group key is order-, case- and duplicate-insensitive",
);
assert(participantGroupKey(shared) !== participantGroupKey(current), "distinct sets differ");
assert(participantGroupKey(unattributed) === null, "no participants means no group");
assert(participantGroupKey(empty) === null, "an empty participant list means no group");
assert(
  participantGroupLabel(participantGroupKey(shared)!) === "me@example.com, other@example.com",
  "label lists the sorted set",
);

const key = (...emails: string[]) =>
  participantGroupKey(
    conversation(
      "x",
      emails.map((email) => participant(email)),
    ),
  )!;
const ordered = [
  key("zed@example.com"),
  key("me@example.com", "zed@example.com"),
  key("alice@example.com"),
  key("me@example.com"),
  key("alice@example.com", "me@example.com"),
]
  .sort((a, b) => compareParticipantGroupKeys(a, b, "ME@example.com"))
  .map(participantGroupLabel);
assert(
  ordered.join(" | ") ===
    [
      "me@example.com",
      "alice@example.com, me@example.com",
      "me@example.com, zed@example.com",
      "alice@example.com",
      "zed@example.com",
    ].join(" | "),
  `groups with the current user come first, then smaller sets, then tuple order: ${ordered.join(" | ")}`,
);
assert(
  compareParticipantGroupKeys(key("me@example.com"), key("alice@example.com"), "") > 0,
  "without a current user, plain tuple order applies",
);

console.log("✓ conversation participant detection, filtering and grouping");
