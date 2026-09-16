<template>
  <div class="usage-graph-switch" role="tablist" aria-label="Usage graph">
    <button
      :aria-selected="modelValue === 'cost'"
      class="usage-graph-switch-button"
      :class="{ 'usage-graph-switch-button-active': modelValue === 'cost' }"
      role="tab"
      @click="emit('update:modelValue', 'cost')"
    >
      cost
    </button>
    <button
      :aria-selected="modelValue === 'context'"
      class="usage-graph-switch-button"
      :class="{ 'usage-graph-switch-button-active': modelValue === 'context' }"
      role="tab"
      @click="emit('update:modelValue', 'context')"
    >
      context
    </button>
  </div>
</template>

<script setup lang="ts">
defineProps<{ modelValue: "cost" | "context" }>();
const emit = defineEmits<{ "update:modelValue": [value: "cost" | "context"] }>();
</script>
