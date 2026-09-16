import type {
  BtwExchange,
  BtwReaderDescriptor,
  Conversation,
  LLMContent,
  Message,
  MessageType,
} from "../types";
import type { TransientState } from "../services/messageStore";

export const parentID = "parent";
export const idleTransient: TransientState = {
  toolProgress: {},
  streamingText: "",
  streamingThinking: "",
  agentWorking: false,
};

export function readerDescriptor(
  childID = "child",
  generation = 2,
  sequenceID = 8,
): BtwReaderDescriptor {
  return {
    conversation_id: childID,
    parent_conversation_id: parentID,
    parent_pointer: { generation, sequence_id: sequenceID },
  };
}

export function childConversation(
  childID = "child",
  createdAt = "2026-08-31T12:00:00Z",
  agentWorking = false,
): Conversation {
  return {
    conversation_id: childID,
    slug: `btw-${childID}`,
    created_at: createdAt,
    agent_working: agentWorking,
  } as Conversation;
}

export const text = (value: string): LLMContent => ({ ID: "", Type: 2, Text: value });

interface MessageOptions {
  conversationID?: string;
  userData?: unknown;
  endOfTurn?: boolean;
}

export function message(
  sequence: number,
  type: MessageType,
  value: string | LLMContent[],
  options: MessageOptions = {},
): Message {
  const content = typeof value === "string" ? [text(value)] : value;
  return {
    message_id: `m${sequence}`,
    conversation_id: options.conversationID ?? "child",
    sequence_id: sequence,
    type,
    generation: 1,
    created_at: `2026-08-31T12:00:${String(sequence).padStart(2, "0")}Z`,
    llm_data: JSON.stringify({
      Role: type === "agent" || type === "error" ? 1 : 0,
      Content: content,
      EndOfTurn: options.endOfTurn,
    }),
    user_data: options.userData === undefined ? null : JSON.stringify(options.userData),
    end_of_turn: options.endOfTurn,
  };
}

export const user = (sequence: number, value: string, options: MessageOptions = {}) =>
  message(sequence, "user", value, options);

export const agent = (sequence: number, value: string, options: MessageOptions = {}) =>
  message(sequence, "agent", value, { ...options, endOfTurn: options.endOfTurn ?? true });

export function exchange(
  exchangeID: string,
  generation: number,
  sequenceID: number,
  createdAt: string,
): BtwExchange {
  return {
    exchange_id: exchangeID,
    parent_conversation_id: parentID,
    status: "completed",
    retryable: false,
    parent_pointer: { generation, sequence_id: sequenceID },
    created_at: createdAt,
    turns: [],
  };
}
