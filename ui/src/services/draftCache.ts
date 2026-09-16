// draftCache mirrors the message composer's autosave into localStorage so a
// reload (or a silently dropped network connection) can never lose unsent
// draft text.
//
// The server-side draft autosave (PUT /conversation/<id>/draft) is
// best-effort and debounced: there is always a window after a keystroke where
// the text lives only in the browser. If the tab reloads in that window, or
// the connection died without the user noticing, every PUT since the last
// successful one is lost. To plug that hole we additionally persist the draft
// to localStorage on EVERY keystroke (synchronous, no network).
//
// On load we read the draft from the server row and from localStorage and keep
// whichever is newer, WITHOUT any server-side schema change. The arbiter is
// the conversation row's existing `updated_at`, which the server bumps on each
// successful PUT /draft. Each keystroke we record, alongside the cached text,
// the server `updated_at` the composer was last in sync with (`basedOn`). On
// load the local copy wins iff its `basedOn` is >= the server's current
// `updated_at` AND its text differs — i.e. the user typed past what the server
// has acknowledged. This naturally covers the lost-connection case: failed
// PUTs never advance `updated_at`, so `basedOn` stays equal to it and the
// locally-typed text is preserved.
//
// We never try to flush localStorage back to the server; on the next keystroke
// the normal autosave carries the merged text forward and the two converge.
//
// Two kinds of session use this cache:
//   * Draft / new-conversation sessions HAVE a server copy, so they reconcile
//     via pickDraft() + `basedOn` as described above.
//   * The next-message composer of an already-sent (non-draft) conversation
//     has NO server-side draft, so its cache entry is authoritative: the
//     caller reads `value` directly and ignores `basedOn` (stored as "").

const PREFIX = "shelley-draft:";

// localStorage key for a draft session. `null` is the special "new
// conversation" session (no server id yet); a lazily-created draft migrates
// its cache to the real id (see ChatInterface).
function cacheKey(id: string | null): string {
  return PREFIX + (id ?? "new");
}

export interface CachedDraft {
  value: string;
  // The server row's `updated_at` the composer was last reconciled with when
  // this cache entry was written. Empty string for the new-conversation
  // session (no server row yet); such an entry always wins on load since any
  // server row that later appears is a fresh draft we just created.
  basedOn: string;
}

export function loadCachedDraft(id: string | null): CachedDraft | null {
  try {
    const raw = localStorage.getItem(cacheKey(id));
    if (raw === null) return null;
    const parsed = JSON.parse(raw);
    if (typeof parsed?.value !== "string" || typeof parsed?.basedOn !== "string") {
      return null;
    }
    return { value: parsed.value, basedOn: parsed.basedOn };
  } catch {
    return null;
  }
}

export function saveCachedDraft(id: string | null, value: string, basedOn: string): void {
  try {
    localStorage.setItem(cacheKey(id), JSON.stringify({ value, basedOn }));
  } catch {
    // Quota or disabled storage: nothing we can do; server autosave remains.
  }
}

export function clearCachedDraft(id: string | null): void {
  try {
    localStorage.removeItem(cacheKey(id));
  } catch {
    // ignore
  }
}

export interface DraftCandidate {
  value: string;
  // The server row's `updated_at`. Empty string when there is no server row
  // yet (new-conversation view).
  updatedAt: string;
}

// pickDraft chooses between the server's copy and the locally-cached copy.
//
// The local copy wins only when the user has typed past what the server has
// acknowledged: its text differs from the server's AND it was based on a
// server state at least as recent as the server's current `updated_at`. The
// >= comparison (rather than >) is deliberate: a dropped connection leaves
// `basedOn` exactly equal to the frozen server `updated_at`, yet the local
// text is the one we must keep. When the server's `updated_at` is strictly
// newer than `basedOn`, the server has changes the cache predates (e.g. an
// edit from another tab), so the server wins.
export function pickDraft(server: DraftCandidate, local: CachedDraft | null): DraftCandidate {
  if (local && local.value !== server.value && local.basedOn >= server.updatedAt) {
    return { value: local.value, updatedAt: server.updatedAt };
  }
  return server;
}

// reconcileComposerDraft decides what (if anything) the message composer should
// be (re)seeded with when the focused conversation, its draft text, or its
// server `updated_at` changes. It is the pure core of ChatInterface's draft
// reconcile watch, extracted so the ordering-sensitive logic can be unit tested.
//
// The bug this guards against: on a slow network the autosave round-trip (PUT
// /draft) and the conversation-row echo that streams the new `updated_at`/draft
// back can arrive out of order. In that window the localStorage mirror's
// `basedOn` still points at the *previous* server state, so pickDraft() looks
// stale and returns the OLDER server snapshot. If we blindly wrote that into the
// composer while the user was mid-keystroke, their text got rewritten and the
// caret jumped to the end (reported on Safari over a high-latency link).
//
// Fix: a session's composer is seeded once on entry; after that a same-session
// echo may only update the composer when doing so cannot clobber in-progress
// typing — either the candidate equals what's already shown, or the user has
// not edited since our last seed (the composer still holds the seeded value).
export interface ComposerReconcileInput {
  // Focused conversation id; null is the new-conversation view.
  conversationId: string | null;
  // Id of a lazily-created draft for the current input session, if any.
  lazyDraftId: string | null;
  // Whether the focused conversation row is a draft.
  isDraft: boolean;
  // The focused conversation row's server draft text and `updated_at`.
  serverDraft: string;
  serverUpdatedAt: string;
  // The localStorage mirror for this session (null if none).
  cached: CachedDraft | null;
  // The composer's live value right now (latest keystrokes).
  composerValue: string;
  // The session id we last seeded the composer for (undefined before any seed).
  lastSeededSession: string | null | undefined;
  // The value we last programmatically wrote into the composer.
  lastSeededValue: string;
}

export interface ComposerReconcileResult {
  // The value to write into the composer.
  value: string;
  // The server `updated_at` this value is reconciled against ("" when none).
  draftSyncedAt: string;
  // The session id to record as seeded.
  seededSession: string | null;
}

export function reconcileComposerDraft(
  input: ComposerReconcileInput,
): ComposerReconcileResult | null {
  const {
    conversationId,
    lazyDraftId,
    isDraft,
    serverDraft,
    serverUpdatedAt,
    cached,
    composerValue,
    lastSeededSession,
    lastSeededValue,
  } = input;

  // A brand-new conversation auto-saving a draft flips conversationId
  // null->draftId mid-typing. That is the same input session, not a switch, so
  // leave the composer (and the user's keystrokes) untouched.
  if (conversationId !== null && conversationId === lazyDraftId) return null;

  const sessionId = conversationId; // null == new-conversation view

  // Compute the candidate value + sync stamp for this session.
  let value: string;
  let draftSyncedAt: string;
  if (isDraft) {
    const picked = pickDraft({ value: serverDraft, updatedAt: serverUpdatedAt }, cached);
    value = picked.value;
    draftSyncedAt = serverUpdatedAt;
  } else if (conversationId === null) {
    const picked = pickDraft({ value: "", updatedAt: "" }, cached);
    value = picked.value;
    draftSyncedAt = "";
  } else {
    // Non-draft conversation: no server-side next-message draft, the local
    // mirror is authoritative.
    value = cached?.value ?? "";
    draftSyncedAt = "";
  }

  // First entry into a session always seeds.
  if (lastSeededSession !== sessionId) {
    return { value, draftSyncedAt, seededSession: sessionId };
  }

  // Same session: this is a server echo. Applying it must not clobber
  // in-progress keystrokes. Safe only when the candidate is already what the
  // composer shows, or the user hasn't edited since our last seed.
  if (value === composerValue) return null;
  if (composerValue === lastSeededValue) {
    return { value, draftSyncedAt, seededSession: sessionId };
  }
  return null;
}
