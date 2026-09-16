<!-- Sub-component of Message.vue: renders the non-text content blocks that
     survive Message.vue's filtering and coalescing pipeline. -->
<template>
  <!-- message_role_user / message_role_assistant: unexpected, show as text -->
  <div
    v-if="ct === 'message_role_user' || ct === 'message_role_assistant'"
    class="msg-unexpected-role"
  >
    <div class="msg-unexpected-role-text">[Unexpected message role content: {{ ct }}]</div>
    <div class="msg-unexpected-content">{{ content.Text || JSON.stringify(content) }}</div>
  </div>

  <!-- thinking -->
  <ThinkingContent v-else-if="ct === 'thinking' && thinkingText" :thinking="thinkingText" />

  <!-- unknown content type -->
  <div v-else-if="ct === 'unknown'" class="msg-unknown-content">
    <div class="text-xs text-secondary msg-unknown-content-label">
      Unknown content type: {{ ct }} (value: {{ content.Type }})
    </div>
    <div v-if="content.MediaType" class="msg-media-section">
      <div class="text-xs text-secondary msg-media-type-label">
        Media Type: {{ content.MediaType }}
      </div>
      <!-- Deliberately not a CommentableImage: this is the diagnostic path for
           content types the UI doesn't recognize, and such an image has no
           filesystem identity, so a comment on it could only cite an image
           endpoint URL the agent cannot open. -->
      <img
        v-if="content.MediaType.startsWith('image/') && content.DisplayImageURL"
        :src="content.DisplayImageURL"
        alt="Tool output image"
        class="rounded border msg-media-image"
      />
    </div>
    <div v-if="displayText" class="text-sm whitespace-pre-wrap break-words">{{ displayText }}</div>
    <details v-if="!displayText && hasOtherData" class="text-xs">
      <summary class="text-secondary msg-raw-content-summary">Show raw content</summary>
      <pre class="msg-raw-content-pre">{{ JSON.stringify(content, null, 2) }}</pre>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../types";
import { getContentType } from "../utils/messageContent";
import ThinkingContent from "./tools/ThinkingContent.vue";

const props = defineProps<{
  content: LLMContent;
}>();

const ct = computed(() => getContentType(props.content.Type));
const thinkingText = computed(() => props.content.Thinking || props.content.Text || "");
const displayText = computed(() => props.content.Text || props.content.Data || "");
const hasOtherData = computed(() =>
  Object.keys(props.content).some(
    (key) => key !== "Type" && key !== "ID" && props.content[key as keyof LLMContent],
  ),
);
</script>
