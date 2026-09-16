import type { BtwExchange, BtwTurn } from "../types";

export type SummaryStorage = Pick<Storage, "getItem" | "setItem">;
type Intent = { after?: string; messageID?: string };

function key(parentID: string): string {
  return `shelley:btw-summary:${parentID}`;
}

function load(storage: SummaryStorage | null, parentID: string): Record<string, Intent> {
  try {
    return JSON.parse(storage?.getItem(key(parentID)) ?? "{}");
  } catch {
    return {};
  }
}

function save(
  storage: SummaryStorage | null,
  parentID: string,
  intents: Record<string, Intent>,
): void {
  storage?.setItem(key(parentID), JSON.stringify(intents));
}

export function requestSummaryIntent(storage: SummaryStorage | null, exchange: BtwExchange): void {
  const parentID = exchange.parent_conversation_id;
  const intents = load(storage, parentID);
  intents[exchange.exchange_id] = {
    after: exchange.turns.filter((turn) => turn.kind === "summary").at(-1)?.id,
  };
  save(storage, parentID, intents);
}

export function resolveSummaryIntent(
  storage: SummaryStorage | null,
  exchange: BtwExchange,
  messageID: string,
): void {
  const parentID = exchange.parent_conversation_id;
  const intents = load(storage, parentID);
  const intent = intents[exchange.exchange_id];
  if (!intent) return;
  intent.messageID = messageID;
  save(storage, parentID, intents);
}

export function claimSummaryIntent(
  storage: SummaryStorage | null,
  exchange: BtwExchange,
): BtwTurn | null {
  const parentID = exchange.parent_conversation_id;
  const intents = load(storage, parentID);
  const intent = intents[exchange.exchange_id];
  if (!intent) return null;

  const turns = exchange.turns.filter((turn) => turn.kind === "summary");
  let turn: BtwTurn | undefined;
  if (intent.messageID) {
    turn = turns.find((candidate) => candidate.id === intent.messageID);
  } else {
    const priorIndex = intent.after
      ? turns.findIndex((candidate) => candidate.id === intent.after)
      : -1;
    if (intent.after && priorIndex < 0) return null;
    turn = turns[priorIndex + 1];
  }
  if (!turn || turn.status !== "completed" || !turn.answer) return null;

  delete intents[exchange.exchange_id];
  save(storage, parentID, intents);
  return turn;
}

export function clearSummaryIntents(storage: SummaryStorage | null, parentID: string): void {
  save(storage, parentID, {});
}
