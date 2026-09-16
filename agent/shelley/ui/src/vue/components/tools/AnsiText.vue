<!-- Renders text that may contain ANSI escape sequences. If SGR color
     codes are present, renders sanitized HTML with inline styles; otherwise
     renders the text with all ANSI sequences stripped (so cursor-movement
     codes like \x1b[1G don't leak stray letters into the view). The <pre>
     element is exposed via a `preRef` template ref for callers that
     auto-scroll. -->
<template>
  <pre v-if="html" ref="preEl" :class="className" v-html="html" />
  <pre v-else ref="preEl" :class="className">{{ fallback }}</pre>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { ansiToHtml, stripAnsi } from "../../../utils/ansi";

const props = defineProps<{ text: string; className?: string }>();
const preEl = ref<HTMLPreElement | null>(null);
const html = computed(() => ansiToHtml(props.text));
// When there's nothing to colorize, render the text with ANSI sequences
// removed rather than the raw text (which would show stray escape letters).
const fallback = computed(() => stripAnsi(props.text));

defineExpose({ preEl });
</script>
