// Vue port of hooks/useMonacoVim.ts. Reuses the framework-agnostic Monaco vim
// helpers in services/monaco.ts; only the React state/effect glue is replaced.
import { ref, watch, onUnmounted, type Ref } from "vue";
import type * as Monaco from "monaco-editor";
import {
  getVimModeEnabled,
  setVimModeEnabled,
  loadMonacoVim,
  ensureVimExtensions,
  setVimQuitHandler,
  clearVimQuitHandlerIf,
} from "../../services/monaco";

const VIM_CHANGE_EVENT = "shelley:monaco-vim-changed";

// Shared, app-wide reactive vim-enabled flag (mirrors the localStorage value
// and the cross-editor custom event used by the React hook).
const enabledRef = ref<boolean>(getVimModeEnabled());
let wired = false;
function ensureWired() {
  if (wired) return;
  wired = true;
  const onChange = () => {
    enabledRef.value = getVimModeEnabled();
  };
  window.addEventListener(VIM_CHANGE_EVENT, onChange);
  window.addEventListener("storage", onChange);
}

export function useVimEnabled(): [Ref<boolean>, (v: boolean) => void] {
  ensureWired();
  const update = (v: boolean) => {
    setVimModeEnabled(v);
    enabledRef.value = v;
    window.dispatchEvent(new CustomEvent(VIM_CHANGE_EVENT));
  };
  return [enabledRef, update];
}

// Framework-free attach/detach state machine for the monaco-vim adapter,
// extracted so the async-race behavior is unit-testable (see
// monacoVim.test.ts). `sync()` is called on every reactive change; it always
// detaches any live adapter and, if the getters say vim should be on, kicks
// off an async attach.
//
// Correctness hinges on the generation counter: every detach (and every new
// attach) bumps it, and an in-flight async attach only completes if its
// generation is still current. A single shared "cancelled" boolean is NOT
// enough — sync() runs overlap while the monaco-vim module loads (e.g.
// enabling vim triggers one run immediately and a second when the v-if'd
// status bar node mounts), and the later run re-arming the flag would
// "un-cancel" the earlier pending attach. That leaked a second adapter on
// the same editor, double-handling every keystroke (doubled characters,
// confused vim mode).
export interface VimAttachDeps {
  getEditor: () => Monaco.editor.IStandaloneCodeEditor | null;
  getStatusBar: () => HTMLElement | null;
  getEnabled: () => boolean;
  onQuit?: (opts: { save: boolean }) => void;
  loadVim?: () => Promise<typeof import("monaco-vim")>;
  ensureExtensions?: () => Promise<void>;
}

export function createVimAttachController(deps: VimAttachDeps): {
  sync: () => void;
  detach: () => void;
} {
  const {
    getEditor,
    getStatusBar,
    getEnabled,
    onQuit,
    loadVim = loadMonacoVim,
    ensureExtensions = ensureVimExtensions,
  } = deps;
  let adapter: { dispose: () => void } | null = null;
  let generation = 0;

  const detach = () => {
    generation++;
    adapter?.dispose();
    adapter = null;
    if (onQuit) clearVimQuitHandlerIf(onQuit);
    const sb = getStatusBar();
    if (sb) sb.replaceChildren();
  };

  const attach = () => {
    const editor = getEditor();
    const statusBarNode = getStatusBar();
    if (!editor || !getEnabled()) return;
    const gen = ++generation;
    loadVim()
      .then(async (mod) => {
        if (gen !== generation) return;
        await ensureExtensions();
        if (gen !== generation) return;
        if (onQuit) setVimQuitHandler(onQuit);
        adapter = mod.initVimMode(editor, statusBarNode ?? undefined);
      })
      .catch((err) => {
        console.error("Failed to load monaco-vim:", err);
      });
  };

  return {
    sync: () => {
      detach();
      attach();
    },
    detach,
  };
}

// Attach a monaco-vim adapter to `editor` whenever vim mode is enabled.
// Pass reactive getters for editor/statusBar/enabled. `onQuit` (if provided)
// fires on :q/:wq/:x/ZZ/ZQ with { save }.
export function useMonacoVim(
  getEditor: () => Monaco.editor.IStandaloneCodeEditor | null,
  getStatusBar: () => HTMLElement | null,
  getEnabled: () => boolean,
  onQuit?: (opts: { save: boolean }) => void,
): void {
  const controller = createVimAttachController({ getEditor, getStatusBar, getEnabled, onQuit });
  watch([getEditor, getStatusBar, getEnabled], controller.sync, { immediate: true });
  onUnmounted(controller.detach);
}
