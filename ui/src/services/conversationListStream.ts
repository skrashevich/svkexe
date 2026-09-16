import type {
  ConversationListPatchEvent,
  ConversationListPatchOp,
  ConversationWithState,
} from "../types";
import { perfCount } from "../utils/perf";

function decodePointer(path: string): string[] {
  if (path === "") return [];
  if (!path.startsWith("/")) {
    throw new Error(`invalid JSON pointer: ${path}`);
  }
  return path
    .slice(1)
    .split("/")
    .map((part) => part.replace(/~1/g, "/").replace(/~0/g, "~"));
}

function cloneValue<T>(value: T): T {
  return value === undefined ? value : JSON.parse(JSON.stringify(value));
}

function getAt(doc: unknown, path: string): unknown {
  let cur = doc;
  for (const part of decodePointer(path)) {
    if (Array.isArray(cur)) {
      const idx = Number(part);
      if (!Number.isInteger(idx) || idx < 0 || idx >= cur.length) {
        throw new Error(`bad array index in patch path: ${path} (len=${cur.length})`);
      }
      cur = cur[idx];
    } else if (cur !== null && typeof cur === "object") {
      cur = (cur as Record<string, unknown>)[part];
    } else {
      throw new Error(`cannot traverse patch path: ${path}`);
    }
  }
  return cur;
}

function encodePointer(parts: string[]): string {
  if (parts.length === 0) return "";
  return `/${parts.map((part) => part.replace(/~/g, "~0").replace(/\//g, "~1")).join("/")}`;
}

function parentAndKey(doc: unknown, path: string): { parent: unknown; key: string } {
  const parts = decodePointer(path);
  if (parts.length === 0) {
    throw new Error("root path has no parent");
  }
  return {
    parent: parts.length === 1 ? doc : getAt(doc, encodePointer(parts.slice(0, -1))),
    key: parts[parts.length - 1],
  };
}

function setAt(doc: unknown, path: string, value: unknown, mustExist: boolean): unknown {
  if (path === "") return cloneValue(value);
  const { parent, key } = parentAndKey(doc, path);
  const nextValue = cloneValue(value);
  if (Array.isArray(parent)) {
    const idx = Number(key);
    if (!Number.isInteger(idx) || idx < 0 || idx > parent.length) {
      throw new Error(`bad array index in patch path: ${path} (len=${parent.length})`);
    }
    if (mustExist && idx >= parent.length) {
      throw new Error(`array index out of range in patch path: ${path} (len=${parent.length})`);
    }
    if (idx === parent.length) {
      parent.push(nextValue);
    } else if (mustExist) {
      parent[idx] = nextValue;
    } else {
      parent.splice(idx, 0, nextValue);
    }
    return doc;
  }
  if (parent !== null && typeof parent === "object") {
    const obj = parent as Record<string, unknown>;
    if (mustExist && !(key in obj)) {
      throw new Error(`missing object key in patch path: ${path}`);
    }
    obj[key] = nextValue;
    return doc;
  }
  throw new Error(`cannot set patch path: ${path}`);
}

function removeAt(doc: unknown, path: string): unknown {
  if (path === "") throw new Error("cannot remove document root");
  const { parent, key } = parentAndKey(doc, path);
  if (Array.isArray(parent)) {
    const idx = Number(key);
    if (!Number.isInteger(idx) || idx < 0 || idx >= parent.length) {
      throw new Error(`bad array index in patch path: ${path} (len=${parent.length})`);
    }
    parent.splice(idx, 1);
    return doc;
  }
  if (parent !== null && typeof parent === "object") {
    delete (parent as Record<string, unknown>)[key];
    return doc;
  }
  throw new Error(`cannot remove patch path: ${path}`);
}

function validateOp(op: ConversationListPatchOp): void {
  if (typeof op.path !== "string") {
    throw new Error(`patch op ${op.op} is missing path`);
  }
  if ((op.op === "add" || op.op === "replace") && !("value" in op)) {
    throw new Error(`patch op ${op.op} is missing value`);
  }
  if (op.op === "move" && typeof op.from !== "string") {
    throw new Error("move patch is missing from");
  }
}

export function applyConversationListPatch(
  state: ConversationWithState[],
  patch: ConversationListPatchOp[],
): ConversationWithState[] {
  const start = performance.now();
  let doc: unknown = cloneValue(state);
  perfCount("list.cloneAll", performance.now() - start);
  for (const op of patch) {
    validateOp(op);
    switch (op.op) {
      case "replace":
        doc = setAt(doc, op.path, op.value, op.path !== "");
        break;
      case "add":
        doc = setAt(doc, op.path, op.value, false);
        break;
      case "remove":
        doc = removeAt(doc, op.path);
        break;
      case "move": {
        const value = cloneValue(getAt(doc, op.from!));
        doc = removeAt(doc, op.from!);
        doc = setAt(doc, op.path, value, false);
        break;
      }
      default: {
        const exhaustive: never = op.op;
        throw new Error(`unsupported patch op: ${exhaustive}`);
      }
    }
  }
  if (!Array.isArray(doc)) {
    throw new Error("conversation list patch did not produce an array");
  }
  return doc as ConversationWithState[];
}

// ConversationListState couples the materialized conversation list with the
// hash it was produced under. The two MUST advance together: the patch stream
// is a strict old_hash->new_hash chain, so applying a patch requires that the
// `list` we apply it to is exactly the one `hash` describes. Keeping them in a
// single value (rather than two independent refs) makes it impossible to
// advance one without the other — the desync that let a patch built against
// new state land on a stale list, corrupting rows (e.g. a /N/preview replace
// landing on the wrong conversation).
export interface ConversationListState {
  list: ConversationWithState[];
  hash: string | null;
}

export type ConversationListReduceResult =
  | { ok: true; state: ConversationListState; removedIds: string[] }
  | { ok: false; reason: "hash-mismatch"; eventOldHash: string | null }
  | { ok: false; reason: "apply-failed"; error: unknown };

// reduceConversationListPatch applies a single patch event to `state`,
// returning the next coupled {list, hash} plus the ids dropped from the list.
// It refuses (ok:false) when the event doesn't anchor to the current hash or
// when the patch can't be applied, so the caller can recover via reconnect
// WITHOUT having mutated either half of the state — preserving the lock
// between list and hash.
export function reduceConversationListPatch(
  state: ConversationListState,
  event: ConversationListPatchEvent,
): ConversationListReduceResult {
  if (!event.reset && (event.old_hash ?? null) !== state.hash) {
    return { ok: false, reason: "hash-mismatch", eventOldHash: event.old_hash ?? null };
  }
  let nextList: ConversationWithState[];
  try {
    nextList = applyConversationListPatch(state.list, event.patch);
  } catch (error) {
    return { ok: false, reason: "apply-failed", error };
  }
  const nextIds = new Set(nextList.map((conv) => conv.conversation_id));
  const removedIds: string[] = [];
  for (const conv of state.list) {
    if (!nextIds.has(conv.conversation_id)) removedIds.push(conv.conversation_id);
  }
  return { ok: true, state: { list: nextList, hash: event.new_hash }, removedIds };
}
