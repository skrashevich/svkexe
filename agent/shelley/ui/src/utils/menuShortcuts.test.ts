import {
  MENU_COMBOS,
  CHAT_INTERFACE_ACTIONS,
  menuShortcutLabel,
  comboMatches,
  matchChatInterfaceAction,
  isMac,
  type MenuActionId,
} from "./menuShortcuts";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

// These tests run in Node (no `navigator`), so isMac === false: labels use the
// "Ctrl+..." form and "mod" combos match Ctrl.
assert(isMac === false, "isMac is false under Node");

// Every action has a combo, and combos are unique by (mod, shift, code).
const ids: MenuActionId[] = [
  "commandPalette",
  "diffs",
  "gitGraph",
  "terminal",
  "archive",
  "export",
  "editAgentsMd",
  "editFile",
  "checkVersion",
];
for (const id of ids) {
  assert(!!MENU_COMBOS[id], `combo exists for ${id}`);
}
const sigs = ids.map((id) => {
  const c = MENU_COMBOS[id];
  return `${c.mod}|${c.shift}|${c.code}`;
});
assert(new Set(sigs).size === sigs.length, `combos are unique: ${sigs.join(", ")}`);

// Non-mac labels.
assert(menuShortcutLabel("commandPalette") === "Ctrl+K", "commandPalette label");
assert(menuShortcutLabel("diffs") === "Ctrl+Shift+D", "diffs label");
assert(menuShortcutLabel("gitGraph") === "Ctrl+Shift+G", "gitGraph label");
assert(menuShortcutLabel("terminal") === "Ctrl+`", "terminal label (ctrl, no shift)");
assert(menuShortcutLabel("archive") === "Ctrl+Shift+A", "archive label");
assert(menuShortcutLabel("export") === "Ctrl+Shift+E", "export label");
assert(menuShortcutLabel("editAgentsMd") === "Ctrl+Shift+,", "editAgentsMd label");
assert(menuShortcutLabel("editFile") === "Ctrl+Shift+P", "editFile label");
assert(menuShortcutLabel("checkVersion") === "Ctrl+Shift+U", "checkVersion label");

// Helper to fabricate a keydown-like event.
function ev(part: Partial<KeyboardEvent>): KeyboardEvent {
  return {
    code: "",
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    altKey: false,
    ...part,
  } as KeyboardEvent;
}

// comboMatches: exact modifier + physical key.
assert(
  comboMatches(ev({ code: "KeyD", ctrlKey: true, shiftKey: true }), MENU_COMBOS.diffs),
  "diffs matches Ctrl+Shift+D",
);
assert(!comboMatches(ev({ code: "KeyD", ctrlKey: true }), MENU_COMBOS.diffs), "diffs needs shift");
assert(
  !comboMatches(
    ev({ code: "KeyD", ctrlKey: true, shiftKey: true, altKey: true }),
    MENU_COMBOS.diffs,
  ),
  "alt disqualifies",
);
assert(
  !comboMatches(
    ev({ code: "KeyD", ctrlKey: true, shiftKey: true, metaKey: true }),
    MENU_COMBOS.diffs,
  ),
  "meta disqualifies off-mac",
);
assert(
  comboMatches(ev({ code: "Backquote", ctrlKey: true }), MENU_COMBOS.terminal),
  "terminal matches Ctrl+`",
);
assert(
  !comboMatches(ev({ code: "Backquote", ctrlKey: true, shiftKey: true }), MENU_COMBOS.terminal),
  "terminal rejects shift",
);

// matchChatInterfaceAction only returns ChatInterface-owned actions.
assert(
  matchChatInterfaceAction(ev({ code: "KeyD", ctrlKey: true, shiftKey: true })) === "diffs",
  "matches diffs",
);
assert(
  matchChatInterfaceAction(ev({ code: "Backquote", ctrlKey: true })) === "terminal",
  "matches terminal",
);
assert(
  matchChatInterfaceAction(ev({ code: "KeyK", ctrlKey: true })) === null,
  "palette is NOT ChatInterface-owned",
);
assert(
  matchChatInterfaceAction(ev({ code: "KeyP", ctrlKey: true, shiftKey: true })) === null,
  "editFile is NOT ChatInterface-owned",
);
assert(
  matchChatInterfaceAction(ev({ code: "KeyZ", ctrlKey: true })) === null,
  "unmapped key is null",
);

// CHAT_INTERFACE_ACTIONS excludes palette + editFile.
assert(!CHAT_INTERFACE_ACTIONS.includes("commandPalette"), "list excludes commandPalette");
assert(!CHAT_INTERFACE_ACTIONS.includes("editFile"), "list excludes editFile");
assert(CHAT_INTERFACE_ACTIONS.length === 7, "seven ChatInterface-owned actions");

if (failed > 0) {
  console.error(`\n${failed} assertion(s) failed, ${passed} passed`);
  process.exit(1);
}
console.log(`\u2713 menuShortcuts: ${passed} assertions passed`);
