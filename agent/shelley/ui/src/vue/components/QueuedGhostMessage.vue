<!-- QueuedGhostMessage renders a queued user message as a ghost/pending item at
     the bottom of the conversation. Derived from conversation.queued_messages
     (NOT a messages row); offers a per-message cancel affordance. -->
<template>
  <div class="message message-user message-queued" data-testid="queued-ghost">
    <div class="message-content" data-testid="message-content">
      <div class="whitespace-pre-wrap break-words">{{ text }}</div>
      <div class="queued-message-badge" data-testid="queued-badge">
        <span class="queued-message-badge-label">
          <svg
            fill="currentColor"
            viewBox="0 0 24 24"
            width="14"
            height="14"
            style="margin-right: 4px; vertical-align: middle"
          >
            <path
              d="M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z"
            />
          </svg>
          Queued
        </span>
        <button
          v-if="onCancel"
          class="queued-message-badge-cancel"
          data-testid="cancel-queued"
          v-tooltip.top="'Cancel queued message'"
          @click.stop="onCancel(queued.id)"
        >
          Cancel
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { type QueuedMessage, queuedMessageText } from "../../types";

const props = defineProps<{
  queued: QueuedMessage;
  onCancel?: (id: string) => void;
}>();

const text = computed(() => queuedMessageText(props.queued));
</script>
