import { Resvg } from "@resvg/resvg-js";
import { mkdir, writeFile } from "node:fs/promises";
import { CriticalActivity } from "../src/activity.js";
import { keyView, type Snapshot } from "../src/model.js";
import type { WarningState } from "../src/pacing.js";
import { renderKey } from "../src/render.js";
import { loadProviderIcons } from "../src/icons.js";

const icon = loadProviderIcons("../shared/provider-icons").Claude;
const activity = new CriticalActivity();
const now = Date.parse("2026-10-13T12:00:00Z");
const warning: WarningState = { level: 0 };
const snapshot = (used: number): Snapshot => ({ status: "ready", providers: [{
  provider_name: "Claude", error: null, is_success: true, needs_reauth: false,
  buckets: [{ id: "session", label: "Session", utilization: used, resets_at: "2026-10-13T14:30:00Z" }]
}] });
const before = keyView(snapshot(70), {}, now, warning);
activity.observe(warning.level, warning.window, -500);
const after = keyView(snapshot(80), {}, now, warning);
await mkdir("dist/critical-frames", { recursive: true });
for (let frame = 0; frame < 88; frame++) {
  const time = frame * 50 - 500;
  if (time === 0) activity.observe(warning.level, warning.window, 0);
  const svg = renderKey(time < 0 ? before : after, icon, 0, time < 0 ? 0 : activity.strength(time));
  await writeFile("dist/critical-frames/" + String(frame).padStart(3, "0") + ".png", new Resvg(svg).render().asPng());
}
console.log("Critical preview: dist/critical-frames/*.png (50 ms per frame, illustrative usage)");
