// Single source of truth for the overflow-menu keyboard shortcuts. Shared by:
//   - ChatOverflowMenu.vue  -> renders the kbd hint on each action row
//   - ChatInterface.vue     -> matches keydown events and invokes the same
//                              handler the menu click would
//
// Command Palette (Cmd/Ctrl+K) and Edit File (Cmd/Ctrl+Shift+P) are matched in
// App.vue, which owns those two modals; their display labels live here too so
// the menu can render a consistent hint for every row.
//
// Design notes (see issue #248):
//   - Browsers reserve most single Cmd/Ctrl+<letter> combos (Cmd+T new tab,
//     Cmd+W close, Cmd+N new window, ...) and never deliver them to the page,
//     so we cannot use them. Cmd/Ctrl+Shift+<letter> is mostly available; a few
//     have overridable browser defaults (bookmark-all, find-previous, ...) that
//     we preventDefault. Cmd/Ctrl+Shift+P is the one hard exception: Firefox
//     reserves it for "New Private Window" and will not deliver it to the page,
//     so Edit File's shortcut is inert in Firefox (use the palette instead).
//   - We match on KeyboardEvent.code (physical key) so Shift-produced glyphs
//     ("D", "<") and keyboard layouts don't matter.

export const isMac =
  typeof navigator !== "undefined" && navigator.platform.toUpperCase().includes("MAC");

export type MenuActionId =
  | "commandPalette"
  | "diffs"
  | "gitGraph"
  | "terminal"
  | "archive"
  | "export"
  | "editAgentsMd"
  | "editFile"
  | "checkVersion";

interface Combo {
  // "mod" = Cmd on mac / Ctrl elsewhere. "ctrl" = Ctrl on both platforms.
  mod: "mod" | "ctrl";
  shift: boolean;
  code: string; // KeyboardEvent.code, e.g. "KeyD", "Comma", "Backquote"
  label: string; // glyph shown in the hint, e.g. "D", ",", "`"
}

export const MENU_COMBOS: Record<MenuActionId, Combo> = {
  commandPalette: { mod: "mod", shift: false, code: "KeyK", label: "K" },
  diffs: { mod: "mod", shift: true, code: "KeyD", label: "D" },
  gitGraph: { mod: "mod", shift: true, code: "KeyG", label: "G" },
  terminal: { mod: "ctrl", shift: false, code: "Backquote", label: "`" },
  archive: { mod: "mod", shift: true, code: "KeyA", label: "A" },
  export: { mod: "mod", shift: true, code: "KeyE", label: "E" },
  editAgentsMd: { mod: "mod", shift: true, code: "Comma", label: "," },
  editFile: { mod: "mod", shift: true, code: "KeyP", label: "P" },
  checkVersion: { mod: "mod", shift: true, code: "KeyU", label: "U" },
};

// Menu actions whose keydown is handled inside ChatInterface (i.e. everything
// except the palette and file finder, which App.vue owns).
export const CHAT_INTERFACE_ACTIONS: readonly MenuActionId[] = [
  "diffs",
  "gitGraph",
  "terminal",
  "archive",
  "export",
  "editAgentsMd",
  "checkVersion",
];

/** Human-readable hint, e.g. "⌘⇧D" on mac or "Ctrl+Shift+D" elsewhere. */
export function menuShortcutLabel(id: MenuActionId): string {
  const c = MENU_COMBOS[id];
  if (isMac) {
    const ctrl = c.mod === "ctrl" ? "\u2303" : ""; // ⌃
    const cmd = c.mod === "mod" ? "\u2318" : ""; // ⌘
    const shift = c.shift ? "\u21e7" : ""; // ⇧
    return `${ctrl}${cmd}${shift}${c.label}`;
  }
  const parts = ["Ctrl"]; // both "mod" and "ctrl" are Ctrl off-mac
  if (c.shift) parts.push("Shift");
  parts.push(c.label);
  return parts.join("+");
}

/** Does a keydown event match this combo? Matches physical key + modifiers. */
export function comboMatches(e: KeyboardEvent, c: Combo): boolean {
  if (e.code !== c.code) return false;
  if (e.altKey) return false;
  if (c.shift !== e.shiftKey) return false;
  if (c.mod === "ctrl") return e.ctrlKey && !e.metaKey;
  // "mod": Cmd-only on mac, Ctrl-only elsewhere.
  return isMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
}

/** Which ChatInterface-owned menu action (if any) does this event trigger? */
export function matchChatInterfaceAction(e: KeyboardEvent): MenuActionId | null {
  for (const id of CHAT_INTERFACE_ACTIONS) {
    if (comboMatches(e, MENU_COMBOS[id])) return id;
  }
  return null;
}

/** Firefox reserves Cmd/Ctrl+Shift+P; the Edit File shortcut is inert there. */
export const isFirefox =
  typeof navigator !== "undefined" && navigator.userAgent.toLowerCase().includes("firefox");
