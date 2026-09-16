import type { BtwToolCall } from "../types";
import { HEADLINE_BUDGET_NARROW, toolHeadline } from "./toolMeta";

function firstBashCommand(command: string): string {
  const firstLine = command
    .split(/\r?\n/)
    .map((part) => part.trim())
    .find((part) => part && !part.startsWith("#"));
  if (!firstLine) return "";
  let line: string = firstLine;
  for (;;) {
    const withoutCd: string = line.replace(/^cd\s+(?:"[^"]*"|'[^']*'|\S+)\s*(?:&&|;)\s*/, "");
    if (withoutCd === line) break;
    line = withoutCd;
  }
  return line.split(/\s*(?:&&|\|\||;|\|)\s*/, 1)[0].trim();
}

function bashTooltip(command: string): string {
  const first = firstBashCommand(command);
  if (!first) return "";
  return toolHeadline("bash", { command: first }, HEADLINE_BUDGET_NARROW);
}

export function btwToolCallTooltip(toolCalls: readonly BtwToolCall[] | undefined): string {
  const counts = new Map<string, number>();
  for (const call of toolCalls || []) {
    if (!call.name || (call.name === "bash" && bashTooltip(call.command || ""))) continue;
    counts.set(call.name, (counts.get(call.name) || 0) + 1);
  }

  const emitted = new Set<string>();
  const labels: string[] = [];
  for (const call of toolCalls || []) {
    if (!call.name) continue;
    const detail = call.name === "bash" ? bashTooltip(call.command || "") : "";
    if (detail) {
      labels.push(`• bash — ${detail}`);
      continue;
    }
    if (emitted.has(call.name)) continue;
    emitted.add(call.name);
    const count = counts.get(call.name) || 0;
    labels.push(`• ${count === 1 ? call.name : `${call.name} ×${count}`}`);
  }
  return labels.join("\n");
}
