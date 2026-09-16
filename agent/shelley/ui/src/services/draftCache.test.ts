// draftCache tests — localStorage mirror of the composer autosave.
//
// Run via `pnpm test` (see scripts/run-tests.mjs).

import {
  loadCachedDraft,
  saveCachedDraft,
  clearCachedDraft,
  pickDraft,
  reconcileComposerDraft,
  type ComposerReconcileInput,
} from "./draftCache";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}
function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

// Minimal in-memory localStorage polyfill for Node.
function installLocalStorage(): void {
  const store = new Map<string, string>();
  const ls = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
    key: (i: number) => Array.from(store.keys())[i] ?? null,
    get length() {
      return store.size;
    },
  };
  Object.defineProperty(globalThis, "localStorage", { value: ls, configurable: true });
}

installLocalStorage();

run("round-trips a cached draft by id", () => {
  saveCachedDraft("c123", "hello", "2026-01-01T00:00:05Z");
  const got = loadCachedDraft("c123");
  assert(got?.value === "hello" && got?.basedOn === "2026-01-01T00:00:05Z", "loads what was saved");
});

run("uses a distinct slot for the new-conversation session", () => {
  saveCachedDraft(null, "new draft", "");
  saveCachedDraft("c1", "existing", "2026-01-01T00:00:09Z");
  assert(loadCachedDraft(null)?.value === "new draft", "null slot isolated");
  assert(loadCachedDraft("c1")?.value === "existing", "id slot isolated");
});

run("returns null for an absent or malformed entry", () => {
  assert(loadCachedDraft("missing") === null, "absent → null");
  localStorage.setItem("shelley-draft:bad", "{not json");
  assert(loadCachedDraft("bad") === null, "malformed → null");
});

run("clearCachedDraft removes the entry", () => {
  saveCachedDraft("c9", "x", "");
  clearCachedDraft("c9");
  assert(loadCachedDraft("c9") === null, "cleared → null");
});

run("pickDraft keeps local edits the server never acknowledged", () => {
  // Connection dropped: server's updated_at is frozen at t5; the user kept
  // typing, so the cache was stamped with that same t5 but holds newer text.
  const server = { value: "saved at t5", updatedAt: "2026-01-01T00:00:05Z" };
  const local = { value: "typed after t5", basedOn: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, local).value === "typed after t5", "unacked local wins");
});

run("pickDraft defers to a server copy that advanced past the cache", () => {
  // Another tab saved at t9; our cache predates it (based on t5).
  const server = { value: "newer from other tab", updatedAt: "2026-01-01T00:00:09Z" };
  const local = { value: "my stale text", basedOn: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, local).value === "newer from other tab", "newer server wins");
});

run("pickDraft defers to the server when text matches", () => {
  const server = { value: "same", updatedAt: "2026-01-01T00:00:05Z" };
  const local = { value: "same", basedOn: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, local).value === "same", "equal text → server (no-op)");
});

run("pickDraft defers to the server when there is no cache", () => {
  const server = { value: "server", updatedAt: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, null).value === "server", "no local → server");
});

run("pickDraft keeps a brand-new-view local draft (empty basedOn)", () => {
  // New-conversation view: no server row yet, so updatedAt is "" and the
  // local entry's basedOn is ""; the local text must survive a reload.
  const server = { value: "", updatedAt: "" };
  const local = { value: "composing something", basedOn: "" };
  assert(pickDraft(server, local).value === "composing something", "new-view local wins");
});

// --- reconcileComposerDraft: the ChatInterface reconcile watch's pure core ---
// These pin the fix for the Safari "cursor jumps to end / text rewritten as I
// type" bug: on a slow network the autosave PUT-ack and the conversation-row
// echo arrive out of order, so a same-session echo must never overwrite the
// composer while the user is mid-keystroke.

function reconcileInput(over: Partial<ComposerReconcileInput>): ComposerReconcileInput {
  return {
    conversationId: "c1",
    lazyDraftId: null,
    isDraft: true,
    serverDraft: "",
    serverUpdatedAt: "2026-01-01T00:00:05Z",
    cached: null,
    composerValue: "",
    lastSeededSession: undefined,
    lastSeededValue: "",
    ...over,
  };
}

run("reconcile seeds the composer on first entry into a session", () => {
  const r = reconcileComposerDraft(
    reconcileInput({ serverDraft: "hello from server", lastSeededSession: undefined }),
  );
  assert(r !== null && r.value === "hello from server", "first entry seeds");
  assert(r!.seededSession === "c1", "records seeded session");
});

run("reconcile leaves the composer untouched during a lazy-draft flip", () => {
  // conversationId flipped null->draftId for the same input session.
  const r = reconcileComposerDraft(
    reconcileInput({ conversationId: "draft9", lazyDraftId: "draft9", composerValue: "typing" }),
  );
  assert(r === null, "lazy-draft flip is a no-op");
});

run("reconcile does NOT clobber in-progress typing on a stale server echo", () => {
  // The user is mid-keystroke ("my new text"); a delayed echo carries an OLDER
  // server snapshot (out-of-order autosave over a slow link). Applying it would
  // rewrite the textarea and jump the caret to the end — the reported bug.
  const r = reconcileComposerDraft(
    reconcileInput({
      serverDraft: "stale server snapshot",
      composerValue: "my new text",
      lastSeededSession: "c1",
      lastSeededValue: "my ne",
    }),
  );
  assert(r === null, "stale echo must not overwrite live keystrokes");
});

run("reconcile applies a same-session echo when the composer is untouched", () => {
  // No local edits since our last seed (composer still holds the seeded value):
  // a server-driven change (e.g. edit from another tab) may safely apply.
  const r = reconcileComposerDraft(
    reconcileInput({
      serverDraft: "updated from another tab",
      serverUpdatedAt: "2026-01-01T00:00:09Z",
      composerValue: "seeded",
      lastSeededSession: "c1",
      lastSeededValue: "seeded",
    }),
  );
  assert(r !== null && r.value === "updated from another tab", "untouched composer accepts echo");
});

run("reconcile is a no-op when the echo already matches the composer", () => {
  const r = reconcileComposerDraft(
    reconcileInput({
      serverDraft: "same text",
      composerValue: "same text",
      lastSeededSession: "c1",
      lastSeededValue: "different-seed",
    }),
  );
  assert(r === null, "echo equal to composer is a no-op");
});

run("reconcile prefers unacked local keystrokes when first seeding a draft", () => {
  // Reload after a dropped connection: server frozen at t5, cache stamped t5
  // with newer text. First seed must restore the user's text, not the server's.
  const r = reconcileComposerDraft(
    reconcileInput({
      serverDraft: "saved at t5",
      serverUpdatedAt: "2026-01-01T00:00:05Z",
      cached: { value: "typed after t5", basedOn: "2026-01-01T00:00:05Z" },
      lastSeededSession: undefined,
    }),
  );
  assert(r !== null && r.value === "typed after t5", "unacked local restored on seed");
});

run("reconcile seeds the new-conversation view from its local mirror", () => {
  const r = reconcileComposerDraft(
    reconcileInput({
      conversationId: null,
      isDraft: false,
      cached: { value: "unsent new draft", basedOn: "" },
      lastSeededSession: undefined,
    }),
  );
  assert(r !== null && r.value === "unsent new draft", "new-view seeds from cache");
  assert(r!.seededSession === null, "new-view session id is null");
});

run("reconcile seeds a non-draft conversation from its authoritative cache once", () => {
  const first = reconcileComposerDraft(
    reconcileInput({
      conversationId: "sent1",
      isDraft: false,
      cached: { value: "next message", basedOn: "" },
      lastSeededSession: undefined,
    }),
  );
  assert(first !== null && first.value === "next message", "non-draft first entry seeds");
  // A later updated_at echo (new agent message) must not wipe the edit.
  const echo = reconcileComposerDraft(
    reconcileInput({
      conversationId: "sent1",
      isDraft: false,
      cached: { value: "next message", basedOn: "" },
      composerValue: "next message and more",
      lastSeededSession: "sent1",
      lastSeededValue: "next message",
    }),
  );
  assert(echo === null, "non-draft echo does not clobber edits");
});

console.log("draftCache: all tests passed");
