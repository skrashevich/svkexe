<!-- Sub-component of Message.vue. Shown on a refusal error (the model declined
     to continue). Unlike a retryable error (which re-runs the same request on
     the same model, and would just be refused again), a refusal offers a
     "Switch to Opus and continue" action: it switches the conversation to a
     more capable model and re-fires the declined request. The accompanying
     error text also tells the user they can use /model to pick a different
     model. Preserves the .error-retry-* classes for styling and exposes the
     data-testid "refusal-continue-button". -->
<template>
  <div class="error-retry-row">
    <button
      type="button"
      class="error-retry-button"
      :disabled="pending"
      data-testid="refusal-continue-button"
      @click="onClick"
    >
      {{ pending ? "Switching\u2026" : "Switch to Opus and continue" }}
    </button>
    <span v-if="error" class="error-retry-error">{{ error }}</span>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { api } from "../../services/api";

const props = defineProps<{ conversationId: string }>();

const pending = ref(false);
const error = ref<string | null>(null);

async function onClick(e: MouseEvent) {
  e.stopPropagation();
  if (pending.value) return;
  pending.value = true;
  error.value = null;
  try {
    // Omit the model so the server switches to the default (Opus) and continues.
    await api.continueConversation(props.conversationId);
    // On success the server switches models and starts a new turn, appending
    // messages so this error is no longer last and this button stops
    // rendering. Clear pending after a fallback delay so the button recovers
    // even if no new message arrives (e.g. transient SSE disconnect).
    window.setTimeout(() => (pending.value = false), 10000);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    pending.value = false;
  }
}
</script>
