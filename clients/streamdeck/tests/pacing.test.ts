import assert from "node:assert/strict";
import { test } from "node:test";
import { pacing, windowSeconds, warningLevel, type WarningState } from "../src/pacing.js";
import { keyView, type Bucket, type Snapshot } from "../src/model.js";
import { renderKey } from "../src/render.js";
const now = Date.parse("2026-09-07T12:00:00Z");
const DAY = 86400;
function bucket(used: number, seconds = 18000, id = "session"): Bucket {
  return { id, label: id, utilization: used, resets_at: new Date(now + seconds * 500).toISOString() };
}
const reading = (b: Bucket, epoch = "a"): Snapshot => ({ status: "ready", providers: [
  { provider_name: "Claude", error: null, is_success: true, needs_reauth: false, buckets: [b],
    fetch_status: { credential_epoch: epoch } }] });

test("session offsets and one-minute tolerance match desktop pacing fixtures", () => {
  for (const [seconds, expected] of [
    [1800, "30m over pace"], [-5400, "1h 30m under pace"], [0, "On pace"],
    [59, "On pace"], [-59, "On pace"], [60, "1m over pace"], [-60, "1m under pace"],
    [3590, "1h over pace"],
  ] as const) {
    const pace = pacing("Claude", bucket(50 + 100 * seconds / 18000), now);
    assert.equal(pace.label, expected);
    assert.equal(pace.expected, 50);
  }
  assert.equal(pacing("Codex", bucket(60, 7 * DAY, "weekly"), now).label, "16h 48m over pace");
  assert.equal(pacing("Cursor", bucket(42.5, 30 * DAY, "plan"), now).label, "2d 6h under pace");
});
test("provider and reported windows agree with the desktop, including leap years and month-end clamping", () => {
  assert.equal(windowSeconds("Codex", { ...bucket(30), label: "Weekly" }), 7 * DAY);
  assert.equal(windowSeconds("Cursor", bucket(50, 7 * DAY, "weekly_grok_bot")), 7 * DAY);
  assert.equal(windowSeconds("Cursor", bucket(30, 30 * DAY, "api")), 30 * DAY);
  for (const [reset, days] of [["2024-03-31T12:00:00Z", 31], ["2025-03-01T12:00:00Z", 28],
    ["2024-03-01T12:00:00Z", 29], ["2026-01-31T12:00:00Z", 31]] as const) {
    assert.equal(windowSeconds("Grok", { ...bucket(30), id: "credits", resets_at: reset }), days * DAY);
  }
  const reported = { ...bucket(50, 28 * DAY, "api"), starts_at: new Date(now - 14 * DAY * 1000).toISOString() };
  assert.equal(windowSeconds("Cursor", reported), 28 * DAY);
  assert.equal(pacing("Cursor", reported, now).expected, 50);
  assert.equal(pacing("Unknown", reported, now).expected, 50);
  for (const start of [null, 12, "bad", reported.resets_at, "2020-01-01T00:00:00Z"]) {
    assert.equal(windowSeconds("Cursor", { ...reported, starts_at: start }), 30 * DAY);
  }
});
test("missing, expired, impossible and status-only windows never invent pace", () => {
  for (const b of [
    { ...bucket(30), resets_at: null }, { ...bucket(30), resets_at: new Date(now).toISOString() },
    { ...bucket(30), resets_at: new Date(now + 18001 * 1000).toISOString() },
    { ...bucket(30), id: "extra" },
  ]) assert.equal(pacing("Claude", b, now).expected, undefined);
  assert.equal(pacing("Unknown", bucket(30), now).expected, undefined);
  const status = { ...bucket(0, 30 * DAY, "on_demand"), status_text: "Enabled" };
  assert.equal(pacing("Cursor", status, now).expected, undefined);
  assert.equal(pacing("Cursor", { ...status, status_text: "$0 / $100" }, now).expected, 50);
  const p = reading(status); p.providers[0].buckets.push(bucket(12));
  assert.equal(keyView(p, {}, now).value, "12%");
  assert.equal(keyView(p, { bucket: "on_demand" }, now).used, undefined);
});
test("warning colors use remaining allowance pressure rather than raw percentage", () => {
  for (const [used, level] of [[50, 0], [55, 1], [62.5, 2], [75, 3]] as const) {
    assert.equal(warningLevel(used, pacing("Claude", bucket(used), now)), level);
  }
  // Late high utilization can be on pace: use capacity safety thresholds only.
  for (const [used, level] of [[90, 0], [95, 1], [99, 2], [100, 3]] as const) {
    assert.equal(warningLevel(used, { expected: used, label: "On pace", pressure: 0 }), level);
  }
  for (const [used, level] of [[49, 0], [50, 1], [75, 2], [90, 3]] as const) {
    assert.equal(warningLevel(used, { label: "Pace unavailable", pressure: 0 }), level);
  }
});
test("warning hysteresis resets for changed windows, account epochs and unavailable data", () => {
  const state: WarningState = { level: 0 };
  assert.equal(keyView(reading(bucket(75)), {}, now, state).color, "#ff5555");
  assert.equal(keyView(reading(bucket(71)), {}, now, state).color, "#ff5555"); // pressure .42 retains Critical
  assert.equal(keyView(reading(bucket(69)), {}, now, state).color, "#ffb86c");
  assert.equal(keyView(reading(bucket(60)), {}, now, state).color, "#ffb86c"); // pressure .20 retains Warning
  assert.equal(keyView(reading(bucket(60), "b"), {}, now, state).color, "#f1fa8c");
  keyView(reading(bucket(75)), {}, now, state);
  assert.equal(keyView(reading({ ...bucket(71), resets_at: new Date(now + 9001 * 1000).toISOString() }), {}, now, state).color, "#ffb86c");
  keyView({ status: "offline", providers: [] }, {}, now, state);
  assert.equal(state.level, 0);
  assert.equal(state.window, undefined);
});
test("used and remaining views keep the same ring, expected marker and pacing", () => {
  const used = keyView(reading(bucket(60)), {}, now);
  const left = keyView(reading(bucket(60)), { display: "remaining" }, now);
  assert.equal(used.used, left.used);
  assert.equal(used.expected, 50);
  assert.equal(left.expected, 50);
  assert.equal(left.value, "40%");
  assert.equal(left.pace, "30m over pace");
  assert.equal(used.color, left.color);
  const svg = renderKey(left);
  assert.ok(svg.includes('data-role="pace-marker"'));
  assert.ok(svg.includes('x1="72.000" y1="115.000"')); // halfway marker at six o'clock
  assert.ok(!svg.includes("<text")); // normal keys are graphical
  const unavailable = renderKey(keyView(reading({ ...bucket(30), resets_at: null }), {}, now));
  assert.ok(!unavailable.includes('data-role="pace-marker"'));
  assert.ok(unavailable.includes('data-role="pace-unavailable"'));
  assert.ok(!renderKey(keyView(reading(bucket(0)), {}, now)).includes('data-role="usage"'));
});
