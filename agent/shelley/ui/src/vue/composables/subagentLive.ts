// subagentLive.ts — live view of a subagent conversation for the parent's
// subagent tool widgets (SubagentTool card + subagent pill).
//
// The unified /api/stream2 already delivers every conversation's events
// (messages, stream deltas, tool progress) to this client, keyed by
// conversation_id, and globalStream routes them all into messageStore —
// including subagent conversations that were never focused. The conversation
// list (provided by App) carries each subagent's authoritative working flag
// and trailing-message preview. This composable just joins the two by slug
// and projects a one-line "what is it doing right now" string.
import { computed, inject, onUnmounted, ref, watch, type InjectionKey, type Ref } from "vue";
import type { ConversationWithState } from "../../types";
import { messageStore } from "../../services/messageStore";
import { subagentActivity } from "../../utils/subagentActivity";

/** The full conversation list (parents + subagents), provided by App. */
export const ConversationsListKey: InjectionKey<Ref<ConversationWithState[]>> = Symbol(
  "shelley-conversations-list",
);

/** The conversation currently rendered by ChatInterface, provided by App.
 *  Used to scope slug lookups to this conversation's own subagents. */
export const CurrentConversationIdKey: InjectionKey<Ref<string | null>> = Symbol(
  "shelley-current-conversation-id",
);

// Mirror of claudetool.sanitizeSlug: lowercase, keep [a-z0-9-], map
// space/underscore to '-', collapse runs, trim. Needed because while the
// tool call is still running we only know the *requested* slug; the server
// sanitizes it before creating the conversation.
export function sanitizeSlug(slug: string): string {
  let s = "";
  for (const r of slug.toLowerCase()) {
    if ((r >= "a" && r <= "z") || (r >= "0" && r <= "9") || r === "-") s += r;
    else if (r === " " || r === "_") s += "-";
  }
  while (s.includes("--")) s = s.split("--").join("-");
  return s.replace(/^-+|-+$/g, "");
}

export interface SubagentLive {
  /** The subagent's conversation row, when found in the list. */
  conv: Ref<ConversationWithState | null>;
  /** Authoritative working flag from the conversation list. */
  working: Ref<boolean>;
  /** One-line live activity (streaming text tail / running tool / preview). */
  activity: Ref<string>;
}

/**
 * Track a subagent conversation by slug (and, once the tool result's display
 * data arrives, by conversation_id — exact and immune to slug-suffix
 * renames). Falls back gracefully to inert refs outside the main app (e.g.
 * the export page provides no conversation list).
 */
export function useSubagentLive(
  slug: Ref<string>,
  conversationId?: Ref<string | undefined>,
): SubagentLive {
  const conversations = inject(ConversationsListKey, null);
  const currentId = inject(CurrentConversationIdKey, null);

  const conv = computed<ConversationWithState | null>(() => {
    const list = conversations?.value;
    if (!list) return null;
    const wantId = conversationId?.value;
    if (wantId) {
      // Exact id known (tool result arrived). Never fall back to the slug
      // heuristic here: if the conversation isn't in the list (archived,
      // pruned), guessing by slug could match a different sibling subagent.
      return list.find((c) => c.conversation_id === wantId) ?? null;
    }
    // No conversation_id yet (tool still running): find this conversation's
    // subagent by slug. The server may have appended a numeric suffix for
    // global uniqueness, so accept `<slug>-<n>` too, newest first.
    const parent = currentId?.value;
    if (!parent) return null;
    const want = sanitizeSlug(slug.value);
    if (!want) return null;
    const subs = list.filter((c) => c.parent_conversation_id === parent);
    const exact = subs.find((c) => c.slug === want);
    if (exact) return exact;
    // `want` is already sanitized to [a-z0-9-], so no regex escaping needed.
    const suffixRe = new RegExp(`^${want}-\\d+$`);
    const suffixed = subs
      .filter((c) => c.slug && suffixRe.test(c.slug))
      .sort((a, b) => b.created_at.localeCompare(a.created_at));
    return suffixed[0] ?? null;
  });

  // Re-render on messageStore updates (messages + transient stream state)
  // for the resolved conversation.
  const tick = ref(0);
  let unsubs: Array<() => void> = [];
  const unsubscribe = () => {
    for (const u of unsubs) u();
    unsubs = [];
  };
  watch(
    () => conv.value?.conversation_id,
    (id) => {
      unsubscribe();
      if (!id) return;
      const bump = () => {
        tick.value++;
      };
      unsubs = [messageStore.subscribe(id, bump), messageStore.subscribeTransient(id, bump)];
    },
    { immediate: true },
  );
  onUnmounted(unsubscribe);

  const working = computed(() => !!conv.value?.working);
  const activity = computed(() => {
    void tick.value; // subscribe to store updates
    const c = conv.value;
    if (!c) return "";
    const t = messageStore.getTransient(c.conversation_id);
    return subagentActivity({
      streamingText: t.streamingText,
      toolProgress: t.toolProgress,
      messages: messageStore.peek(c.conversation_id)?.messages,
      preview: c.preview,
    });
  });

  return { conv, working, activity };
}

/** Client-side navigation to a conversation, mirroring SubagentTool's
 *  pushState + popstate pattern (App handles popstate). */
export function navigateToConversationSlug(slug: string): void {
  window.history.pushState({}, "", `/c/${slug}`);
  window.dispatchEvent(new PopStateEvent("popstate"));
}
