import type { BtwExchange, BtwReaderDescriptor } from "../../types";
import type { CoalescedItem } from "./coalesce";
import { nextTick } from "vue";

export function btwGenerationStartAnchorKey(generation: number): string {
  return `generation:${generation}:start`;
}

export interface BtwAnchor {
  key: string;
  item?: CoalescedItem;
}

export function btwAnchor(
  pointer: BtwReaderDescriptor["parent_pointer"],
  items: readonly CoalescedItem[],
): BtwAnchor {
  for (let index = items.length - 1; index >= 0; index--) {
    const item = items[index];
    if (item.generation === pointer.generation && item.sourceSequenceID <= pointer.sequence_id) {
      return { key: item.anchorKey, item };
    }
  }
  return { key: btwGenerationStartAnchorKey(pointer.generation) };
}

export function btwExchangesByAnchor(
  exchanges: readonly BtwExchange[],
  items: readonly CoalescedItem[],
): Map<string, BtwExchange[]> {
  const byAnchor = new Map<string, BtwExchange[]>();
  for (const exchange of exchanges) {
    const anchor = btwAnchor(exchange.parent_pointer, items);
    const anchored = byAnchor.get(anchor.key) ?? [];
    anchored.push(exchange);
    byAnchor.set(anchor.key, anchored);
  }
  for (const anchored of byAnchor.values()) {
    anchored.sort(
      (a, b) =>
        Date.parse(a.created_at) - Date.parse(b.created_at) ||
        a.exchange_id.localeCompare(b.exchange_id),
    );
  }
  return byAnchor;
}

export function latestBtwExchange(exchanges: readonly BtwExchange[]): BtwExchange | undefined {
  return exchanges[0];
}

function findBtwExchange(exchangeId: string, root: ParentNode): HTMLElement | undefined {
  return Array.from(root.querySelectorAll<HTMLElement>("[data-btw-exchange-id]")).find(
    (element) => element.dataset.btwExchangeId === exchangeId,
  );
}

export function scrollToBtwExchange(exchangeId: string, root: ParentNode = document): boolean {
  const target = findBtwExchange(exchangeId, root);
  if (!target) return false;
  target.scrollIntoView({ behavior: "smooth", block: "center" });
  return true;
}

export async function focusBtwFollowUp(
  exchangeId: string,
  root: ParentNode = document,
): Promise<boolean> {
  const exchange = findBtwExchange(exchangeId, root);
  const toggle = exchange?.querySelector<HTMLButtonElement>(".btw-inline-label");
  if (toggle?.ariaExpanded === "false") {
    toggle.click();
    await nextTick();
  }
  const input = exchange?.querySelector<HTMLTextAreaElement>("[data-btw-follow-up]");
  if (!input || input.disabled) return false;
  input.focus();
  return true;
}
