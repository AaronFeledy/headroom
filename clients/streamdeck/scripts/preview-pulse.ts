import { Resvg } from "@resvg/resvg-js";
import { mkdir, writeFile } from "node:fs/promises";
import { UsageActivity } from "../src/activity.js";
import { renderKey } from "../src/render.js";
import { loadProviderIcons } from "../src/icons.js";
const icon = loadProviderIcons("../shared/provider-icons").Claude;
const activity = new UsageActivity();
activity.observe(35, "demo", -1000);
activity.observe(36, "demo", 0);
const view = { provider: "Claude", label: "Session", value: "36%", used: 36, expected: 50, color: "#bd93f9", detail: "" };
await mkdir("dist/pulse-frames", { recursive: true });
for (let frame = 0; frame < 40; frame++) {
  const svg = renderKey(view, icon, activity.strength(frame * 50));
  await writeFile("dist/pulse-frames/" + String(frame).padStart(3, "0") + ".png", new Resvg(svg).render().asPng());
}
console.log("Pulse preview: dist/pulse-frames/*.png (50 ms per frame, illustrative usage)");
