<!-- Performance HUD (behind the `performance-hud` feature flag). Displays
     live recomputation counters collected via utils/perf.ts perfCount().
     The HUD polls a non-reactive counter Map on an interval, so displaying
     it adds no reactive dependencies to the hot paths it observes. Rows show
     the rate over the last poll window (Δ/s), total count, and total ms for
     timed counters. A second section lists recent main-thread long tasks
     (>50ms) with the counters that incremented since the previous one — the
     usual suspects for the stall. Observation is always on (utils/perf.ts);
     the HUD only displays it.
     Console access (always on, flag or not): window.__shelleyPerf -->
<template>
  <Teleport to="body">
    <div class="perf-hud" :class="{ collapsed }">
      <div class="perf-hud-header" @click="toggleCollapsed">
        <span class="perf-hud-title">perf</span>
        <span v-if="collapsed" class="perf-hud-mini">{{ miniSummary }}</span>
        <span v-else class="perf-hud-actions" @click.stop>
          <button class="perf-hud-btn" title="Reset counters" @click="reset">reset</button>
          <button class="perf-hud-btn" title="Copy snapshot JSON" @click="copy">
            {{ copied ? "copied" : "copy" }}
          </button>
          <button
            class="perf-hud-btn"
            :title="paused ? 'Resume' : 'Pause'"
            @click="paused = !paused"
          >
            {{ paused ? "resume" : "pause" }}
          </button>
        </span>
      </div>
      <template v-if="!collapsed && loads.length > 0">
        <div class="perf-hud-section">
          conversation loads
          <span class="perf-hud-section-hint">source · messages · total, newest first</span>
        </div>
        <table class="perf-hud-table">
          <tbody>
            <tr
              v-for="load in loads"
              :key="`${load.completedAt}-${load.conversationId}`"
              class="perf-hud-load"
              :title="loadBreakdown(load)"
            >
              <td class="perf-hud-load-when">{{ formatEpochAge(load.completedAt) }}</td>
              <td class="perf-hud-name perf-hud-load-source">{{ sourceLabel(load.source) }}</td>
              <td>{{ formatCount(load.messages) }} msg</td>
              <td class="perf-hud-load-total">{{ formatDuration(load.totalMs) }}</td>
            </tr>
          </tbody>
        </table>
      </template>
      <template v-if="!collapsed && longTasks.length > 0">
        <div class="perf-hud-section">
          long tasks
          <span class="perf-hud-section-hint">main-thread blocks &gt;50ms, newest first</span>
        </div>
        <table class="perf-hud-table">
          <tbody>
            <tr v-for="task in longTasks" :key="task.startMs" class="perf-hud-longtask">
              <td class="perf-hud-longtask-when">{{ formatAge(task.startMs) }}</td>
              <td class="perf-hud-longtask-dur">{{ Math.round(task.durMs) }}ms</td>
              <td class="perf-hud-name perf-hud-longtask-suspects">
                {{ task.suspects.join(" ") || "?" }}
              </td>
            </tr>
          </tbody>
        </table>
      </template>
      <table v-if="!collapsed" class="perf-hud-table">
        <thead>
          <tr>
            <th class="perf-hud-name">counter</th>
            <th title="events per second over the last poll window">Δ/s</th>
            <th title="total count since load/reset">total</th>
            <th title="total milliseconds spent (timed counters only)">ms</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.name" :class="{ 'perf-hud-hot': row.rate > 0 }">
            <td class="perf-hud-name">{{ row.name }}</td>
            <td>{{ row.rate > 0 ? row.rate.toFixed(row.rate >= 10 ? 0 : 1) : "·" }}</td>
            <td>{{ row.count }}</td>
            <td>{{ row.totalMs > 0 ? formatMs(row.totalMs) : "" }}</td>
          </tr>
          <tr v-if="rows.length === 0">
            <td colspan="4" class="perf-hud-empty">no counters yet</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!collapsed" class="perf-hud-footer">
        __shelleyPerf.{snapshot,delta,loads,longTasks,log,reset}
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue";
import {
  perfSnapshot,
  perfReset,
  perfLongTasks,
  perfConversationLoads,
  type ConversationLoad,
  type ConversationLoadSource,
  type LongTask,
  type PerfCounter,
} from "../../utils/perf";

const POLL_MS = 500;
const countFormatter = new Intl.NumberFormat();

interface Row {
  name: string;
  count: number;
  totalMs: number;
  rate: number; // events/sec over the last poll window
}

const rows = ref<Row[]>([]);
const loads = ref<ConversationLoad[]>([]);
const longTasks = ref<LongTask[]>([]);
// performance.now() at the last sample; drives long-task age display without
// a reactive clock.
const sampledAt = ref(performance.now());
// The HUD sits over the conversation drawer, so remember collapse across loads.
const COLLAPSED_KEY = "perf-hud:collapsed";
const collapsed = ref(localStorage.getItem(COLLAPSED_KEY) === "true");
const paused = ref(false);
const copied = ref(false);

function toggleCollapsed(): void {
  collapsed.value = !collapsed.value;
  localStorage.setItem(COLLAPSED_KEY, String(collapsed.value));
}

let prev: Record<string, PerfCounter> = perfSnapshot();
let prevAt = performance.now();
let timer: number | undefined;

const miniSummary = computed(() => {
  const hot = rows.value.filter((r) => r.rate > 0);
  const lt = longTasks.value[0];
  const latestLoad = loads.value[0];
  // Surface a just-happened long task even when collapsed.
  const jank = lt && sampledAt.value - lt.startMs < 5000 ? ` ⚠${Math.round(lt.durMs)}ms` : "";
  const load =
    latestLoad && Date.now() - latestLoad.completedAt < 10000
      ? ` · ${sourceLabel(latestLoad.source)} ${formatDuration(latestLoad.totalMs)}`
      : "";
  if (hot.length === 0) return `idle${load}${jank}`;
  const total = hot.reduce((sum, r) => sum + r.rate, 0);
  return `${hot.length} hot, ${total.toFixed(0)}/s${load}${jank}`;
});

function sample(): void {
  const now = performance.now();
  const snap = perfSnapshot();
  const dtSec = Math.max((now - prevAt) / 1000, 1e-6);
  const next: Row[] = [];
  for (const [name, c] of Object.entries(snap)) {
    const p = prev[name];
    const rate = (c.count - (p?.count ?? 0)) / dtSec;
    next.push({ name, count: c.count, totalMs: c.totalMs, rate });
  }
  // Active counters first (by rate), then by total count.
  next.sort((a, b) => b.rate - a.rate || b.count - a.count || a.name.localeCompare(b.name));
  rows.value = next;
  // Newest load/long task first. The full 20-entry buffers remain available
  // through __shelleyPerf.{loads,longTasks}().
  loads.value = perfConversationLoads().reverse().slice(0, 6);
  longTasks.value = perfLongTasks().reverse().slice(0, 6);
  sampledAt.value = now;
  prev = snap;
  prevAt = now;
}

function reset(): void {
  perfReset();
  prev = {};
  prevAt = performance.now();
  rows.value = [];
  loads.value = [];
  longTasks.value = [];
}

async function copy(): Promise<void> {
  try {
    await navigator.clipboard.writeText(
      JSON.stringify(
        {
          counters: perfSnapshot(),
          loads: perfConversationLoads(),
          longTasks: perfLongTasks(),
        },
        null,
        2,
      ),
    );
    copied.value = true;
    setTimeout(() => (copied.value = false), 1200);
  } catch (e) {
    console.warn("perf-hud: clipboard write failed", e);
  }
}

function sourceLabel(source: ConversationLoadSource): string {
  switch (source) {
    case "memory":
      return "Memory";
    case "indexeddb":
      return "IndexedDB";
    case "incremental":
      return "Cache + tail";
    case "network":
      return "Network";
  }
}

function formatCount(value: number): string {
  return countFormatter.format(value);
}

function formatDuration(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(ms >= 10000 ? 1 : 2)}s`;
  return `${Math.round(ms)}ms`;
}

function formatEpochAge(completedAt: number): string {
  const ageSec = Math.max(0, (Date.now() - completedAt) / 1000);
  if (ageSec < 60) return `${Math.round(ageSec)}s`;
  return `${Math.floor(ageSec / 60)}m`;
}

function loadBreakdown(load: ConversationLoad): string {
  const parts = [
    `hydrate ${formatDuration(load.hydrateMs)}`,
    `fetch ${formatDuration(load.fetchMs)}`,
    `render ${formatDuration(load.renderMs)}`,
  ];
  if (load.bytes > 0) parts.push(`${(load.bytes / (1024 * 1024)).toFixed(1)} MB decoded`);
  return parts.join(" · ");
}

// Timed counters accumulate float milliseconds (performance.now() has µs
// fractions, coarsened to ~100µs by Spectre mitigations — sums over many
// calls are still sound). Keep sub-ms precision visible instead of rounding
// small-but-real totals down to "0".
function formatMs(ms: number): string {
  if (ms >= 10000) return `${(ms / 1000).toFixed(1)}s`;
  if (ms >= 100) return `${Math.round(ms)}`;
  if (ms >= 1) return ms.toFixed(1);
  return ms.toFixed(2);
}

/** Age of a long task relative to the last sample, e.g. "3s" or "2m". */
function formatAge(startMs: number): string {
  const ageSec = Math.max(0, (sampledAt.value - startMs) / 1000);
  if (ageSec < 60) return `${Math.round(ageSec)}s`;
  return `${Math.floor(ageSec / 60)}m`;
}

onMounted(() => {
  sample();
  timer = window.setInterval(() => {
    if (!paused.value) sample();
  }, POLL_MS);
});

onUnmounted(() => {
  if (timer !== undefined) window.clearInterval(timer);
});
</script>
