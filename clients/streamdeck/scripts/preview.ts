import { Resvg } from "@resvg/resvg-js";
import { writeFile } from "node:fs/promises";
import { keyView, type Settings, type Snapshot } from "../src/model.js";
import { renderKey } from "../src/render.js";
import { loadProviderIcons } from "../src/icons.js";
const icons = loadProviderIcons("../shared/provider-icons");
const now = Date.parse("2026-10-13T12:00:00Z");
function view(provider: string, id: string, used: number, duration: number, progress = .5, config: Settings = {}) {
  const snapshot: Snapshot = { status: "ready", providers: [{ provider_name: provider, error: null,
    is_success: true, needs_reauth: false, buckets: [{ id, label: id[0].toUpperCase() + id.slice(1),
      utilization: used, resets_at: new Date(now + duration * (1 - progress) * 1000).toISOString(),
      starts_at: new Date(now - duration * progress * 1000).toISOString() }] }] };
  return keyView(snapshot, { provider, ...config }, now);
}
const noTiming: Snapshot = { status: "ready", providers: [{ provider_name: "Grok", error: null, is_success: true,
  needs_reauth: false, buckets: [{ id: "credits", label: "Credits", utilization: 16, resets_at: null }] }] };
const views = [
  view("Claude", "session", 35, 18000),
  view("Codex", "weekly", 60, 7 * 86400, .5, { display: "remaining" }),
  view("Cursor", "plan", 70, 30 * 86400),
  view("Grok", "credits", 80, 30 * 86400),
  view("Claude", "session", 50, 18000),
  view("Codex", "weekly", 90, 7 * 86400, .9),
  view("Cursor", "api", 0, 30 * 86400),
  keyView(noTiming, { provider: "Grok" }, now),
  keyView({ status: "offline", providers: [] }, { provider: "Claude" }, now),
  keyView({ status: "ready", providers: [{ ...noTiming.providers[0], provider_name: "Codex", needs_reauth: true }] }, { provider: "Codex" }, now),
  keyView(noTiming, { provider: "Grok", bucket: "weekly" }, now),
  view("Grok", "credits", 100, 30 * 86400),
];
let body = '<rect width="672" height="528" rx="24" fill="#11121a"/>';
views.forEach((view, i) => { body += '<g transform="translate(' + (24 + i % 4 * 160) + ',' + (24 + Math.floor(i / 4) * 168) + ')">' + renderKey(view, icons[view.provider]) + '</g>'; });
for (const [name, width, height] of [["key-preview", 672, 528], ["key-preview-72px", 336, 264]] as const) {
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="' + width + '" height="' + height + '" viewBox="0 0 672 528">' + body + '</svg>';
  await writeFile("dist/" + name + ".png", new Resvg(svg).render().asPng());
}
console.log("Illustrative tray-style key previews: dist/key-preview.png and dist/key-preview-72px.png");
