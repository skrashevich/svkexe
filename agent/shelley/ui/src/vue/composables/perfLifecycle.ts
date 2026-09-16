// Component churn counters for the performance HUD (see utils/perf.ts).
//
// Counts only — deliberately NOT timed. Vue runs mounted/updated hooks in a
// post-flush queue: an onBeforeMount→onMounted (or onBeforeUpdate→onUpdated)
// span stretches from the component's own render start to the end of the
// ENTIRE flush, so with N components mounting in one flush you get N nearly
// identical overlapping spans and a sum ~N× wall time (measured: 829 Message
// mounts "totalling" 470s during a 5s conversation load). Vue exposes no
// per-component self-time hook; wall-clock cost of flushes shows up honestly
// in the browser.longtask counter instead, and explicitly-bracketed work
// (perfWrap) is where the ms column comes from.
import { onMounted, onUpdated } from "vue";
import { perfCount } from "../../utils/perf";

/** Count this component's `<prefix>.mount` and `<prefix>.update` events.
 *  Must be called during setup. */
export function usePerfLifecycle(prefix: string): void {
  onMounted(() => perfCount(`${prefix}.mount`));
  onUpdated(() => perfCount(`${prefix}.update`));
}
