<!-- Sub-component of Message.vue. Renders a "modelchange" marker recorded when
     the conversation switches models via the /model command (or shows the
     /model command's informational output). User-visible only; never sent to
     the LLM.

     A real switch (model and/or reasoning changed) gets a customized pill: the
     old → new model names as chips with an arrow, plus a reasoning chip when
     that changed. Purely informational output (bare /model status, errors,
     "already using") falls back to the plain text notice. -->
<template>
  <div
    v-if="isSwitch"
    class="message message-gitinfo msg-modelchange-container msg-modelchange-switch"
    data-testid="message-modelchange"
    role="status"
  >
    <span class="msg-modelchange-icon">🤖</span>
    <template v-if="modelChanged">
      <span class="msg-modelchange-chip msg-modelchange-from">{{ fromName }}</span>
      <span class="msg-modelchange-arrow" aria-hidden="true">→</span>
      <span class="msg-modelchange-chip msg-modelchange-to">{{ toName }}</span>
    </template>
    <span v-if="reasoningChanged" class="msg-modelchange-reasoning">
      <span class="msg-modelchange-reasoning-label">reasoning</span>
      <span class="msg-modelchange-chip">{{ reasoningTo }}</span>
    </span>
  </div>
  <div
    v-else
    class="message message-gitinfo msg-modelchange-container"
    data-testid="message-modelchange"
    role="status"
  >
    <span class="msg-modelchange-icon">🤖</span>
    <span class="msg-modelchange-text">{{ text }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Message as MessageType } from "../../types";

const props = defineProps<{ message: MessageType }>();

interface ModelChangeData {
  from?: string;
  to?: string;
  from_display?: string;
  to_display?: string;
  reasoning_from?: string;
  reasoning_to?: string;
  text?: string;
}

const data = computed<ModelChangeData>(() => {
  if (!props.message.user_data) return {};
  try {
    return typeof props.message.user_data === "string"
      ? JSON.parse(props.message.user_data)
      : (props.message.user_data as ModelChangeData);
  } catch {
    return {};
  }
});

const text = computed(() => data.value.text || "Model changed");

// A model switch records a non-empty `to`; a reasoning-only change records
// reasoning_to. Informational markers (bare /model, errors) have neither.
const modelChanged = computed(() => !!data.value.to);
const reasoningChanged = computed(() => !!data.value.reasoning_to);
const isSwitch = computed(() => modelChanged.value || reasoningChanged.value);

const fromName = computed(() => data.value.from_display || data.value.from || "");
const toName = computed(() => data.value.to_display || data.value.to || "");
const reasoningTo = computed(() => data.value.reasoning_to || "");
</script>
