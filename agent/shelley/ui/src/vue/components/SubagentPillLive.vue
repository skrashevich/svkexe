<!-- Live activity chip for a subagent tool pill. Rendered as a sibling of
     the pill button (a button can't nest interactive content): when the
     subagent conversation is currently working it shows the shared working
     ring plus a one-line "what it's doing" tail sourced from the unified
     stream (via messageStore + the conversation list). Clicking it opens the
     subagent conversation; modified clicks (cmd/ctrl/middle) open in a new
     tab. -->
<template>
  <a
    v-if="showLive"
    class="subagent-pill-live"
    data-testid="subagent-pill-live"
    :href="`/c/${liveSlug}`"
    :title="`Open subagent '${liveSlug}'`"
    @click="onClick"
  >
    <span class="working-indicator" aria-hidden="true" />
    <span class="subagent-pill-activity">{{ activity || "working\u2026" }}</span>
  </a>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useSubagentLive, navigateToConversationSlug } from "../composables/subagentLive";

const props = defineProps<{
  toolInput?: unknown;
  display?: unknown;
  isRunning?: boolean;
}>();

const input = computed<{ slug?: string }>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null
    ? (props.toolInput as { slug?: string })
    : {},
);
const displayData = computed<{ slug?: string; conversation_id?: string }>(() =>
  typeof props.display === "object" && props.display !== null
    ? (props.display as { slug?: string; conversation_id?: string })
    : {},
);

const slug = computed(() => displayData.value.slug || input.value.slug || "");
const { conv, working, activity } = useSubagentLive(
  slug,
  computed(() => displayData.value.conversation_id),
);
const showLive = computed(() => working.value || (!!props.isRunning && !!conv.value));
const liveSlug = computed(() => conv.value?.slug || slug.value);

function onClick(e: MouseEvent) {
  // Let the browser handle cmd/ctrl/shift/middle-click (open in new tab).
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
  e.preventDefault();
  e.stopPropagation();
  navigateToConversationSlug(liveSlug.value);
}
</script>
