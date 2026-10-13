import assert from "node:assert/strict";
import { test } from "node:test";
import { decodeUsage, keyView, resetLabel, type Provider, type Snapshot } from "../src/model.js";
import { renderKey } from "../src/render.js";

export const provider: Provider = {
  provider_name: "Codex", error: null, is_success: true, needs_reauth: false, auth: { state: "signed_in" },
  buckets: [{ id: "session", label: "Session", utilization: 42, resets_at: "2026-10-13T08:00:00Z" },
    { id: "weekly", label: "Weekly", utilization: 68, resets_at: null }],
};
const now = Date.parse("2026-10-13T06:00:00Z");
const snapshot: Snapshot = { status: "ready", providers: [provider] };
test("ChatGPT keys select stable bucket IDs and show used or remaining with reset time", () => {
  assert.deepEqual(keyView(snapshot, { provider: "Codex" }, now),
    { provider: "ChatGPT", label: "Session", display: "used", value: "42%", detail: "Reset 2h 0m", used: 42,
      expected: 60, pace: "54m under pace", color: "#bd93f9" });
  assert.equal(keyView(snapshot, { provider: "Codex", bucket: "weekly", display: "remaining" }, now).value, "32%");
  assert.equal(keyView(snapshot, { provider: "Codex", bucket: "api" }, now).value, "No meter");
  assert.equal(keyView(snapshot, { provider: "Claude" }, now).value, "Disabled");
});
test("Cursor auto is a real meter, distinct from first available", () => {
  const p = { ...provider, provider_name: "Cursor", buckets: [
    { ...provider.buckets[0], id: "plan" }, { ...provider.buckets[1], id: "auto" }] };
  assert.equal(keyView({ status: "ready", providers: [p] }, { provider: "Cursor", bucket: "auto" }).value, "68%");
});
test("unavailable, signed-out and retrying states never show cached usage", () => {
  for (const status of ["offline", "missing", "invalid"] as const) {
    assert.equal(keyView({ ...snapshot, status }, { provider: "Codex" }).used, undefined);
  }
  for (const patch of [{ error: "secret error", is_success: false }, { needs_reauth: true },
    { auth: { state: "expired" } }, { fetch_status: { failure_kind: "transient" } }]) {
    const view = keyView({ status: "ready", providers: [{ ...provider, ...patch }] }, { provider: "Codex" });
    assert.equal(view.used, undefined);
    assert.ok(!JSON.stringify(view).includes("secret"));
  }
});
test("usage validation rejects malformed data and duplicate meters", () => {
  assert.equal(decodeUsage(JSON.stringify([provider]))[0].provider_name, "Codex");
  for (const input of [{}, null, [provider, provider], [{ ...provider, is_success: false }],
    [{ ...provider, buckets: [{ ...provider.buckets[0], utilization: null }] }],
    [{ ...provider, buckets: [{ ...provider.buckets[0], utilization: 101 }] }],
    [{ ...provider, buckets: [provider.buckets[0], provider.buckets[0]] }]]) {
    assert.throws(() => decodeUsage(JSON.stringify(input)));
  }
});
test("reset labels handle overdue and absent timestamps without inventing zero usage", () => {
  assert.equal(resetLabel(null, now), "No reset time");
  assert.equal(resetLabel("2026-10-13T05:00:00Z", now), "Reset due");
  assert.equal(resetLabel("2026-10-13T06:00:01Z", now), "Reset 1m");
  assert.equal(resetLabel("2026-10-15T09:00:00Z", now), "Reset 2d 3h");
});
test("SVG labels cannot inject markup or control characters", () => {
  const image = renderKey({ provider: "<script>", label: "\u202e<&\"", value: "0%", detail: "safe", used: 0 });
  assert.ok(!image.includes("<script>"));
  assert.ok(!image.includes("\u202e"));
  assert.ok(image.includes("&lt;&amp;&quot;"));
  assert.ok(!image.includes('data-role="usage"'));
});
