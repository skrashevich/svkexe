// Tool-progress distribution without prop drilling.
//
// ChatInterface holds one reactive Record<tool_use_id, ToolProgress> that is
// replaced wholesale on every tool-progress SSE event (~1/s per running
// tool). Passing it down as a prop gave every message/tool component a
// changed prop identity per event: measured with the performance-hud
// counters, one bash progress event re-rendered all ~800 message components
// in a 250-turn conversation (~80ms main-thread block, repeated for the whole
// life of the tool call).
//
// Instead, ChatInterface provide()s the ref once and each tool component
// injects a computed of ITS OWN tool's output. Vue computeds only notify
// dependents when their value changes, so a progress event re-renders exactly
// the component whose output grew — everything else sees an unchanged
// (usually undefined) value and stays put.
import { computed, inject, provide, type ComputedRef, type InjectionKey, type Ref } from "vue";
import type { ToolProgress } from "../../types";

export const ToolProgressKey: InjectionKey<Ref<Record<string, ToolProgress>>> =
  Symbol("tool-progress");

export function provideToolProgress(progress: Ref<Record<string, ToolProgress>>): void {
  provide(ToolProgressKey, progress);
}

const EMPTY: Record<string, ToolProgress> = {};

/** Streaming output for one tool call; undefined when absent or not provided
 *  (e.g. tool cards rendered outside a conversation). */
export function useToolStreamingOutput(
  toolUseId: () => string | undefined,
): ComputedRef<string | undefined> {
  const progress = inject(ToolProgressKey, undefined);
  return computed(() => {
    const id = toolUseId();
    if (!id) return undefined;
    return (progress?.value ?? EMPTY)[id]?.output;
  });
}
