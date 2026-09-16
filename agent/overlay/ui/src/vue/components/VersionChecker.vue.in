<!-- This combined runtime is distributed as a complete binary by its operator. -->
<template>
  <Modal :is-open="isOpen" title="Agent version" class-name="version-modal" @close="emit('close')">
    <div v-if="isLoading" class="version-loading">Reading agent version...</div>
    <template v-else-if="versionInfo">
      <div class="version-info-row">
        <span class="version-label">Running build:</span>
        <span class="version-value">{{ versionInfo.current_version }}</span>
      </div>
      <div v-if="versionInfo.error" class="version-error">{{ versionInfo.error }}</div>
    </template>
    <p class="version-customized-note">
      Build an updated binary from this agent package, stop the agent, replace the binary
      and start it with the same database and configuration. Conversation history is preserved.
    </p>
  </Modal>
</template>

<script setup lang="ts">
import type { VersionInfo } from "../../types";
import Modal from "./Modal.vue";

defineProps<{
  isOpen: boolean;
  versionInfo: VersionInfo | null;
  isLoading: boolean;
}>();
const emit = defineEmits<{ (e: "close"): void }>();
</script>
