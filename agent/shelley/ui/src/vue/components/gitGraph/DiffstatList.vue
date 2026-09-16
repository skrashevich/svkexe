<!-- Vue port of the DiffstatList subcomponent in components/GitGraphViewer.tsx.
     Compact git diff --stat style list. Each row: the path, +/- counts, and
     a tiny green/red bar scaled by the biggest row.
     The whole row is a link that opens the diff viewer on that file (href
     supports middle/modifier click to open in a new tab). -->
<template>
  <ul class="git-graph-diffstat-list">
    <li v-for="f in rows" :key="f.path" class="git-graph-diffstat-row">
      <a
        class="git-graph-diffstat-link"
        :href="fileHref(f.path)"
        :title="f.path"
        @click="onClick($event, f.path)"
      >
        <span class="git-graph-diffstat-path">{{ f.path }}</span>
        <span class="git-graph-diffstat-counts">
          <span v-if="f.binary" class="git-graph-diffstat-binary">bin</span>
          <template v-else>
            <span v-if="f.additions > 0" class="git-graph-diffstat-ins">+{{ f.additions }}</span>
            <span v-if="f.deletions > 0" class="git-graph-diffstat-del">−{{ f.deletions }}</span>
          </template>
        </span>
        <span class="git-graph-diffstat-bar" aria-hidden="true">
          <span class="git-graph-diffstat-ins">{{ "+".repeat(f.adds) }}</span>
          <span class="git-graph-diffstat-del">{{ "−".repeat(f.dels) }}</span>
        </span>
      </a>
    </li>
  </ul>
</template>

<script setup lang="ts">
import { computed } from "vue";

interface DiffFile {
  path: string;
  additions: number;
  deletions: number;
  binary: boolean;
}

const props = defineProps<{ files: DiffFile[]; fileHref: (path: string) => string }>();
const emit = defineEmits<{ (e: "open", path: string): void }>();

function onClick(e: MouseEvent, path: string) {
  // Let the browser handle modifier/middle-click so users can open the diff
  // in a new tab/window.
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
  e.preventDefault();
  emit("open", path);
}

// 40 chars max wide for the bar; cap smaller for short paths.
const BAR_CHARS = 24;

const rows = computed(() => {
  const maxTotal = Math.max(
    1,
    ...props.files.map((f) => (f.binary ? 0 : f.additions + f.deletions)),
  );
  return props.files.map((f) => {
    const total = f.additions + f.deletions;
    const scale = total === 0 ? 0 : Math.max(1, Math.round((total / maxTotal) * BAR_CHARS));
    const adds =
      total === 0
        ? 0
        : Math.max(f.additions > 0 ? 1 : 0, Math.round((f.additions / Math.max(1, total)) * scale));
    const dels = Math.max(0, scale - adds);
    return { ...f, adds, dels };
  });
});
</script>
