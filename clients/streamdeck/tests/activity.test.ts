import assert from "node:assert/strict";
import { test } from "node:test";
import { UsageActivity, CriticalActivity, PULSE_DURATION_MS, CRITICAL_DURATION_MS } from "../src/activity.js";
import { UsageKeyDisplay, type AnimationClock } from "../src/display.js";
import type { Snapshot } from "../src/model.js";
import { renderKey } from "../src/render.js";

test("each raw increase pulses once; steady readings and pace changes do not restart it", () => {
  const activity = new UsageActivity();
  activity.observe(25, "window", 0);
  assert.equal(activity.active, false);
  activity.observe(25.01, "window", 100);
  assert.equal(activity.active, true);
  assert.equal(activity.strength(250), 1);
  activity.observe(25.01, "window", 300);
  assert.ok(activity.strength(600) < 1);
  assert.equal(activity.strength(1000), 0);
  assert.equal(activity.active, false);
  activity.observe(25.02, "window", 1100);
  assert.equal(activity.strength(1250), 1);
});
test("baseline changes and unavailable readings do not look like new activity", () => {
  for (const next of [{ used: 80, window: "new-window" }, { used: 10, window: "window" },
    { used: undefined, window: undefined }]) {
    const activity = new UsageActivity();
    activity.observe(30, "window", 0);
    activity.observe(31, "window", 100);
    activity.observe(next.used, next.window, 150);
    assert.equal(activity.active, false);
    assert.equal(activity.strength(250), 0);
  }
  const recovered = new UsageActivity();
  recovered.observe(10, "account-a", 0);
  recovered.observe(80, "account-b", 50);
  assert.equal(recovered.active, false);
  recovered.observe(undefined, undefined, 100);
  recovered.observe(90, "account-b", 150);
  assert.equal(recovered.active, false);
});
class FakeClock implements AnimationClock {
  value = Date.parse("2026-10-13T12:00:00Z");
  jobs = new Map<number, { at: number; callback: () => void }>();
  id = 0;
  now = () => this.value;
  later = (callback: () => void, delay: number) => {
    const id = ++this.id;
    this.jobs.set(id, { at: this.value + delay, callback });
    return () => { this.jobs.delete(id); };
  };
  advance(ms: number): void {
    const end = this.value + ms;
    while (true) {
      const next = [...this.jobs].sort((a, b) => a[1].at - b[1].at)[0];
      if (!next || next[1].at > end) break;
      this.value = next[1].at;
      this.jobs.delete(next[0]);
      next[1].callback();
    }
    this.value = end;
  }
}
const flush = () => new Promise<void>(resolve => setImmediate(resolve));
const svg = (image: string) => Buffer.from(image.split(",")[1], "base64").toString();
const reading = (used: number, epoch = "account"): Snapshot => ({ status: "ready", providers: [
  { provider_name: "Claude", error: null, is_success: true, needs_reauth: false,
    fetch_status: { credential_epoch: epoch },
    buckets: [{ id: "session", label: "Session", utilization: used, resets_at: "2026-10-13T14:30:00Z" }] }] });

test("key animation ends on the exact resting image and leaves no idle timers", async () => {
  const time = new FakeClock(), images: string[] = [];
  const display = new UsageKeyDisplay({}, {}, async image => { images.push(svg(image)); }, time);
  display.update(reading(35)); await flush();
  assert.equal(time.jobs.size, 0);
  display.update(reading(36)); await flush();
  const resting = images.at(-1);
  time.advance(150); await flush();
  assert.ok(images.at(-1)!.includes('data-role="activity-pulse"'));
  assert.ok(images.at(-1)!.includes('opacity="0.350"'));
  time.advance(PULSE_DURATION_MS - 150); await flush();
  assert.equal(images.at(-1), resting);
  assert.equal(time.jobs.size, 0);
  const count = images.length;
  time.advance(10000); await flush();
  assert.equal(images.length, count);
  display.dispose();
});
test("hiding, changing settings, switching accounts and outages cancel animation", async () => {
  for (const stop of ["hide", "settings", "account", "offline"] as const) {
    const time = new FakeClock(), images: string[] = [];
    const display = new UsageKeyDisplay({}, {}, async image => { images.push(svg(image)); }, time);
    display.update(reading(35)); await flush();
    display.update(reading(36));
    time.advance(150); await flush();
    assert.ok(images.at(-1)!.includes('data-role="activity-pulse"'));
    if (stop === "hide") display.dispose();
    if (stop === "settings") display.configure({ provider: "Codex" });
    if (stop === "account") display.update(reading(80, "another-account"));
    if (stop === "offline") display.update({ status: "offline", providers: [] });
    await flush();
    assert.equal(time.jobs.size, 0);
    if (stop !== "hide") assert.ok(!images.at(-1)!.includes('data-role="activity-pulse"'));
    const count = images.length;
    time.advance(2000); await flush();
    assert.equal(images.length, count);
    display.dispose();
  }
});
test("slow device writes coalesce animation frames and cannot replay old settings", async () => {
  const time = new FakeClock(), sent: string[] = [];
  const releases: (() => void)[] = [];
  const display = new UsageKeyDisplay({}, {}, image => {
    sent.push(svg(image));
    return new Promise(resolve => releases.push(resolve));
  }, time);
  display.update(reading(35));
  display.update(reading(36));
  time.advance(500);
  assert.equal(sent.length, 1); // loading write is still in progress
  display.configure({ provider: "Codex" });
  releases.shift()!(); await flush();
  assert.equal(sent.length, 2);
  assert.ok(sent[1].includes("ChatGPT"));
  assert.ok(!sent[1].includes('data-role="activity-pulse"'));
  display.dispose();
  releases.shift()!(); await flush();
  time.advance(2000);
  assert.equal(sent.length, 2);
});
test("the pulse preserves usage geometry, warning color and pace marker", () => {
  const view = { provider: "Claude", label: "Session", value: "60%", detail: "", used: 60, expected: 50, color: "#f1fa8c" };
  const normal = renderKey(view), peak = renderKey(view, undefined, 1);
  assert.ok(!normal.includes('data-role="activity-pulse"'));
  assert.ok(peak.includes('data-role="activity-pulse"'));
  assert.equal(normal.match(/stroke-dasharray="[^"]+"/)?.[0], peak.match(/stroke-dasharray="[^"]+"/)?.[0]);
  assert.equal(normal.match(/<g data-role="pace-marker">.*?<\/g>/)?.[0], peak.match(/<g data-role="pace-marker">.*?<\/g>/)?.[0]);
  assert.ok(peak.includes('stroke="#f1fa8c"'));
  assert.ok(!peak.includes("<text"));
  assert.equal(renderKey(view, undefined, NaN), normal);
  assert.ok(!renderKey({ ...view, used: undefined }, undefined, 1).includes('data-role="activity-pulse"'));
});

test("critical entry plays three beats once, holds through steady readings, and rearms after recovery", () => {
  const activity = new CriticalActivity();
  activity.observe(2, "window", 0);
  activity.observe(3, "window", 100);
  assert.equal(activity.active, true);
  for (const peak of [250, 1050, 1850]) assert.equal(activity.strength(peak), 1);
  activity.observe(3, "window", 2200);
  assert.equal(activity.strength(2500), 0);
  assert.equal(activity.active, false);
  activity.observe(3, "window", 5000);
  assert.equal(activity.active, false);
  activity.observe(2, "window", 5100);
  activity.observe(3, "window", 5200);
  assert.equal(activity.strength(5350), 1);
});

test("an already-critical baseline, another window/account and reconnects never manufacture a critical transition", () => {
  const activity = new CriticalActivity();
  activity.observe(3, "account-a/window", 0);
  assert.equal(activity.active, false);
  activity.observe(2, "account-a/window", 100);
  activity.observe(3, "account-a/window", 200);
  assert.equal(activity.active, true);
  activity.observe(3, "account-b/window", 250);
  assert.equal(activity.active, false);
  activity.observe(2, "account-b/window", 300);
  activity.observe(3, "account-b/next-window", 400);
  assert.equal(activity.active, false);
  activity.observe(2, "account-b/next-window", 500);
  activity.observe(3, "account-b/next-window", 600);
  activity.observe(0, undefined, 650);
  assert.equal(activity.active, false);
  activity.observe(3, "account-b/next-window", 700);
  assert.equal(activity.active, false);
  activity.observe(2, "account-b/next-window", 800);
  activity.observe(3, "account-b/next-window", 900);
  activity.observe(2, "account-b/next-window", 950);
  assert.equal(activity.active, false);
});

test("critical animation wins over usage pulses and ends with the latest red meter without idle timers", async () => {
  const time = new FakeClock(), images: string[] = [];
  const display = new UsageKeyDisplay({}, {}, async image => { images.push(svg(image)); }, time);
  display.update(reading(70)); await flush();
  display.update(reading(80)); await flush();
  time.advance(150); await flush();
  assert.ok(images.at(-1)!.includes('data-role="critical-pulse"'));
  assert.ok(images.at(-1)!.includes('opacity="0.950"'));
  assert.ok(!images.at(-1)!.includes('data-role="activity-pulse"'));
  time.advance(1850);
  display.update(reading(81)); await flush();
  time.advance(CRITICAL_DURATION_MS - 2000); await flush();
  assert.ok(!images.at(-1)!.includes('data-role="critical-pulse"'));
  assert.ok(!images.at(-1)!.includes('data-role="activity-pulse"'));
  assert.ok(images.at(-1)!.includes("81%"));
  assert.ok(images.at(-1)!.includes('stroke="#ff5555"'));
  assert.equal(time.jobs.size, 0);
  const count = images.length;
  time.advance(5000); await flush();
  assert.equal(images.length, count);
  // Continuing critical usage still gets the usual gentle activity indication.
  display.update(reading(82));
  time.advance(150); await flush();
  assert.ok(images.at(-1)!.includes('data-role="activity-pulse"'));
  assert.ok(!images.at(-1)!.includes('data-role="critical-pulse"'));
  display.dispose();
});

test("critical recovery follows the real warning hysteresis before another alert can fire", async () => {
  const time = new FakeClock(), images: string[] = [];
  const display = new UsageKeyDisplay({}, {}, async image => { images.push(svg(image)); }, time);
  display.update(reading(70));
  display.update(reading(80));
  time.advance(CRITICAL_DURATION_MS); await flush();
  // 74% at halfway is below entry (75%) but above critical recovery (70%).
  display.update(reading(74)); await flush();
  assert.ok(images.at(-1)!.includes('stroke="#ff5555"'));
  display.update(reading(80));
  time.advance(150); await flush();
  assert.ok(!images.at(-1)!.includes('data-role="critical-pulse"'));
  display.update(reading(69)); await flush();
  assert.ok(images.at(-1)!.includes('stroke="#ffb86c"'));
  display.update(reading(80));
  time.advance(150); await flush();
  assert.ok(images.at(-1)!.includes('data-role="critical-pulse"'));
  display.dispose();
});

test("hiding, settings, account/window changes, outages and recovery stop the critical animation", async () => {
  for (const stop of ["hide", "settings", "account", "window", "offline", "recovery"] as const) {
    const time = new FakeClock(), images: string[] = [];
    const display = new UsageKeyDisplay({}, {}, async image => { images.push(svg(image)); }, time);
    display.update(reading(70));
    display.update(reading(80));
    time.advance(150); await flush();
    assert.ok(images.at(-1)!.includes('data-role="critical-pulse"'));
    if (stop === "hide") display.dispose();
    if (stop === "settings") { display.configure({}); display.update(reading(80)); }
    if (stop === "account") display.update(reading(80, "another-account"));
    if (stop === "window") {
      const next = reading(80);
      next.providers[0].buckets[0].resets_at = "2026-10-13T15:30:00Z";
      display.update(next);
    }
    if (stop === "offline") display.update({ status: "offline", providers: [] });
    if (stop === "recovery") display.update(reading(69));
    await flush();
    assert.equal(time.jobs.size, 0);
    if (stop !== "hide") assert.ok(!images.at(-1)!.includes('data-role="critical-pulse"'));
    const count = images.length;
    time.advance(5000); await flush();
    assert.equal(images.length, count);
    display.dispose();
  }
});

test("critical glow leaves the pace marker, logo and usage amount readable and fixed", () => {
  const icon = "data:image/png;base64,aGVhZHJvb20=";
  const view = { provider: "Claude", label: "Session", value: "80%", detail: "", used: 80, expected: 50, color: "#ff5555" };
  const normal = renderKey(view, icon), peak = renderKey(view, icon, 1, 1);
  assert.ok(peak.includes('data-role="critical-pulse"'));
  assert.ok(!peak.includes('data-role="activity-pulse"'));
  assert.ok(!peak.includes("<text"));
  assert.equal(normal.match(/stroke-dasharray="[^"]+"/)?.[0], peak.match(/stroke-dasharray="[^"]+"/)?.[0]);
  assert.equal(normal.match(/<g data-role="pace-marker">.*?<\/g>/)?.[0], peak.match(/<g data-role="pace-marker">.*?<\/g>/)?.[0]);
  assert.equal(normal.match(/<image .*?\/>/)?.[0], peak.match(/<image .*?\/>/)?.[0]);
  assert.equal(renderKey(view, icon, 0, NaN), normal);
  assert.ok(!renderKey({ ...view, used: undefined }, icon, 0, 1).includes('data-role="critical-pulse"'));
});
