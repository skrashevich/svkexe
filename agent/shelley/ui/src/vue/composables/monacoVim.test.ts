// monacoVim.test.ts — regression tests for the vim adapter attach/detach race.
//
// When the monaco-vim module loads asynchronously, overlapping sync() runs
// (e.g. toggling edit → comment → edit before the chunk arrives, or the
// enabled flag plus the v-if'd status bar node changing in quick succession)
// used to leak a second live adapter on the same editor. Two adapters both
// handle every keystroke: characters get doubled and vim-mode state gets
// confused. These tests drive createVimAttachController with a controllable
// fake loader and assert exactly one adapter survives, no matter how the
// async resolutions interleave.
//
// Self-executing on import (see scripts/run-tests.mjs).

import { createVimAttachController } from "./monacoVim";
import type * as Monaco from "monaco-editor";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

async function run(name: string, fn: () => void | Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

// ---- Fakes -----------------------------------------------------------------

interface FakeAdapter {
  disposed: boolean;
  dispose: () => void;
}

function makeHarness() {
  const adapters: FakeAdapter[] = [];
  const fakeModule = {
    initVimMode: () => {
      const a: FakeAdapter = {
        disposed: false,
        dispose() {
          this.disposed = true;
        },
      };
      adapters.push(a);
      return a;
    },
  } as unknown as typeof import("monaco-vim");

  // Each loadVim() call gets its own manually-resolvable promise so tests
  // control the interleaving of module-load completions.
  const pendingLoads: Array<() => void> = [];
  const loadVim = () =>
    new Promise<typeof import("monaco-vim")>((resolve) => {
      pendingLoads.push(() => resolve(fakeModule));
    });

  const fakeEditor = {} as Monaco.editor.IStandaloneCodeEditor;
  let enabled = false;

  const controller = createVimAttachController({
    getEditor: () => fakeEditor,
    getStatusBar: () => null,
    getEnabled: () => enabled,
    loadVim,
    ensureExtensions: () => Promise.resolve(),
  });

  return {
    adapters,
    pendingLoads,
    controller,
    setEnabled(v: boolean) {
      enabled = v;
    },
    liveCount: () => adapters.filter((a) => !a.disposed).length,
    // Let the .then chains after a resolved load run to completion.
    settle: () => new Promise((r) => setTimeout(r, 0)),
  };
}

// ---- Tests -----------------------------------------------------------------

await run("attaches one adapter when enabled", async () => {
  const h = makeHarness();
  h.setEnabled(true);
  h.controller.sync();
  assert(h.pendingLoads.length === 1, "one load started");
  h.pendingLoads[0]();
  await h.settle();
  assert(h.liveCount() === 1, `expected 1 live adapter, got ${h.liveCount()}`);
});

await run("overlapping syncs while module loads leak no extra adapter", async () => {
  const h = makeHarness();
  // enable → disable → enable, all before the module load resolves. With the
  // old shared-boolean cancellation, the third sync() re-armed the flag and
  // "un-cancelled" the first pending attach: both resolved and two live
  // adapters double-handled every keystroke.
  h.setEnabled(true);
  h.controller.sync(); // attach #1 pending
  h.setEnabled(false);
  h.controller.sync(); // detach (no attach)
  h.setEnabled(true);
  h.controller.sync(); // attach #2 pending

  // Resolve both pending loads, oldest first.
  for (const resolve of h.pendingLoads) resolve();
  await h.settle();

  assert(h.liveCount() === 1, `expected 1 live adapter, got ${h.liveCount()}`);
});

await run("rapid repeated syncs while enabled leave exactly one adapter", async () => {
  const h = makeHarness();
  h.setEnabled(true);
  // Several syncs in a row (editor recreate, status bar mount, etc.) before
  // any module load resolves.
  for (let i = 0; i < 5; i++) h.controller.sync();
  for (const resolve of h.pendingLoads) resolve();
  await h.settle();
  assert(h.liveCount() === 1, `expected 1 live adapter, got ${h.liveCount()}`);
});

await run("out-of-order load resolution keeps only the latest attach", async () => {
  const h = makeHarness();
  h.setEnabled(true);
  h.controller.sync(); // attach #1 pending
  h.controller.sync(); // attach #2 pending
  // Resolve newest first, then the stale one.
  h.pendingLoads[1]();
  await h.settle();
  h.pendingLoads[0]();
  await h.settle();
  assert(h.liveCount() === 1, `expected 1 live adapter, got ${h.liveCount()}`);
});

await run("detach after pending attach prevents any adapter", async () => {
  const h = makeHarness();
  h.setEnabled(true);
  h.controller.sync(); // attach pending
  h.controller.detach(); // unmount before the module arrives
  h.pendingLoads[0]();
  await h.settle();
  assert(h.liveCount() === 0, `expected 0 live adapters, got ${h.liveCount()}`);
});

console.log("monacoVim tests passed");
