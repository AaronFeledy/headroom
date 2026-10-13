import { readFileSync } from "node:fs";
import path from "node:path";

// Reuse the tray's bundled provider icons.
export function loadProviderIcons(directory: string): Record<string, string> {
  return Object.fromEntries([["Claude", "claude"], ["ChatGPT", "codex"], ["Cursor", "cursor"], ["Grok", "grok"]]
    .map(([display, file]) => [display, "data:image/png;base64," + readFileSync(path.join(directory, file + ".png")).toString("base64")]));
}
