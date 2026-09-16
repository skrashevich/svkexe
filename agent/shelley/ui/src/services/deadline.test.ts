// deadline tests — the contract the cache layer relies on to stay live.
//
// The point of withDeadline is that the caller always gets an answer even
// though the underlying operation cannot be cancelled. That makes what happens
// to a LATE result the interesting part: an IndexedDB connection that arrives
// after we stopped waiting must be closed (an abandoned connection at the
// current version would itself block the next version bump), and a late error
// must still be reported (a cache that silently stops working with nothing in
// the console is the failure mode this whole area exists to prevent).
//
// Run via `pnpm test` (see scripts/run-tests.mjs).

import { withDeadline, isDeadlineExceeded, DeadlineExceededError, pendingWaits } from "./deadline";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}
async function run(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function main(): Promise<void> {
  await run("resolves normally when the operation beats the deadline", async () => {
    const got = await withDeadline(Promise.resolve("ok"), 1000, { what: "test" });
    assert(got === "ok", "value passes through");
  });

  await run("rejects with DeadlineExceededError when it doesn't", async () => {
    let err: unknown;
    try {
      await withDeadline(new Promise(() => {}), 20, { what: "stalled-op" });
    } catch (e) {
      err = e;
    }
    assert(err instanceof DeadlineExceededError, "throws the typed error");
    assert(isDeadlineExceeded(err), "and the predicate recognizes it");
    assert(String(err).includes("stalled-op"), `error names the operation: ${String(err)}`);
  });

  await run("a real failure is reported as itself, not as a timeout", async () => {
    // Callers branch on isDeadlineExceeded to decide whether to back off, so
    // conflating the two would apply a cooldown to errors that should retry.
    let err: unknown;
    try {
      await withDeadline(Promise.reject(new Error("quota")), 1000, { what: "test" });
    } catch (e) {
      err = e;
    }
    assert(!isDeadlineExceeded(err), "not a deadline error");
    assert(String(err).includes("quota"), "the original error survives");
  });

  await run("a value that lands after the deadline is handed to onLate", async () => {
    let late: string | null = null;
    let resolveIt!: (v: string) => void;
    const p = new Promise<string>((r) => {
      resolveIt = r;
    });
    await withDeadline(p, 20, { what: "test", onLate: (v) => (late = v) }).catch(() => {});
    assert(late === null, "not called before the value arrives");
    resolveIt("arrived");
    await sleep(0);
    assert(late === "arrived", "the late value is handed over for disposal");
  });

  await run("an error that lands after the deadline is handed to onLateError", async () => {
    let seen: unknown;
    let rejectIt!: (e: unknown) => void;
    const p = new Promise<string>((_r, rej) => {
      rejectIt = rej;
    });
    await withDeadline(p, 20, { what: "test", onLateError: (e) => (seen = e) }).catch(() => {});
    rejectIt(new Error("late boom"));
    await sleep(0);
    assert(String(seen).includes("late boom"), "the late error is surfaced, not swallowed");
  });

  await run("onLate is not called when the operation wins", async () => {
    let called = false;
    const got = await withDeadline(Promise.resolve(7), 1000, {
      what: "test",
      onLate: () => (called = true),
    });
    await sleep(30);
    assert(got === 7 && !called, "a timely value is returned, not disposed of");
  });

  await run("an in-flight wait is visible while it is still waiting", async () => {
    // The gap this closes: every cacheDiag event is recorded when a decision
    // COMPLETES, so a tab that is currently hanging reports stats() === {}.
    // That emptiness is what made the original bug so hard to place. A wait
    // must be inspectable while it is still a wait.
    let release: (v: string) => void = () => {};
    const p = new Promise<string>((res) => {
      release = res;
    });
    const wait = withDeadline(p, 5_000, { what: "indexedDB.open" });
    const inflight = pendingWaits();
    assert(inflight.length === 1, `expected 1 in-flight wait, got ${inflight.length}`);
    assert(inflight[0].what === "indexedDB.open", `wrong label: ${inflight[0].what}`);
    assert(inflight[0].deadlineMs === 5_000, "the deadline is reported");
    assert(inflight[0].elapsedMs >= 0, "elapsed time is reported");
    release("done");
    await wait;
    assert(pendingWaits().length === 0, "a settled wait is no longer in flight");
  });

  await run("a wait that misses its deadline stops being in flight", async () => {
    // Leaking registry entries would turn the diagnostic into a liar, showing
    // phantom waits that outlive the code that started them.
    let err: unknown;
    try {
      await withDeadline(new Promise<string>(() => {}), 20, { what: "GET /api/cache-key" });
    } catch (e) {
      err = e;
    }
    assert(isDeadlineExceeded(err), "the deadline fired");
    assert(pendingWaits().length === 0, `registry leaked: ${JSON.stringify(pendingWaits())}`);
  });

  await run("a rejected wait stops being in flight", async () => {
    let err: unknown;
    try {
      await withDeadline(Promise.reject(new Error("boom")), 5_000, { what: "x" });
    } catch (e) {
      err = e;
    }
    assert(String(err).includes("boom"), "the real error propagates");
    assert(pendingWaits().length === 0, "registry cleared on rejection");
  });

  console.log("\ndeadline tests passed");
}

await main();
