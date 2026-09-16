import { isAutoExpandTool } from "../../utils/toolMeta";

export type ToolCardPlaceholderKind = "bash" | "generic" | "media" | "output-iframe" | "patch";

export function toolCardPlaceholderKind(
  toolName: string,
  toolInput?: unknown,
  display?: unknown,
): ToolCardPlaceholderKind {
  if (toolName === "bash" || toolName === "shell") return "bash";
  if (toolName === "patch") return "patch";
  if (toolName === "output_iframe") return "output-iframe";
  if (
    toolName === "screenshot" ||
    toolName === "browser_take_screenshot" ||
    toolName === "read_image" ||
    (toolName === "browser" &&
      typeof toolInput === "object" &&
      toolInput !== null &&
      "action" in toolInput &&
      (toolInput as { action?: unknown }).action === "screenshot") ||
    (toolName === "llm_one_shot" && isAutoExpandTool(toolName, toolInput, display))
  ) {
    return "media";
  }
  return "generic";
}
