import assert from "node:assert/strict";
import { test } from "node:test";
import { decodeUsage, keyView, type Bucket, type Snapshot } from "../src/model.js";
import { pacing, statusOnly, type WarningState } from "../src/pacing.js";
import { renderKey } from "../src/render.js";

const now = Date.parse("2026-10-29T00:00:00Z");
const meter: Bucket = { id: "on_demand", label: "Extra", utilization: 0, status_text: "Enabled",
  starts_at: "2026-10-25T12:00:00Z", resets_at: "2026-11-01T12:00:00Z" };
const reading = (bucket: Bucket): Snapshot => ({ status: "ready", providers: [{ provider_name: "Cursor",
  error: null, is_success: true, needs_reauth: false, buckets: [bucket] }] });

test("explicit status and future measurements suppress rings, pace and warning history", () => {
  for (const measurement of ["status", "future", "", false, 17, {}]) {
    const bucket = { ...meter, id: "weekly", utilization: 99, measurement };
    const warning: WarningState = { window: "old-account", level: 3 };
    const view = keyView(reading(bucket), { provider: "Cursor", bucket: "weekly" }, now, warning);
    assert.equal(statusOnly(bucket), true);
    assert.equal(view.used, undefined);
    assert.equal(view.expected, undefined);
    assert.equal(pacing("Cursor", bucket, now).expected, undefined);
    assert.deepEqual(warning, { window: undefined, level: 0 });
    assert.ok(!renderKey(view).includes('data-role="usage"'));
  }
});

test("explicit percent preserves measured zero while null and absent retain legacy status inference", () => {
  for (const bucket of [meter, { ...meter, measurement: null }]) {
    assert.equal(keyView(reading(bucket), { provider: "Cursor" }, now).used, undefined);
  }
  const measured = { ...meter, measurement: "percent" };
  const view = keyView(reading(measured), { provider: "Cursor" }, now);
  assert.equal(view.used, 0);
  assert.equal(view.value, "0%");
  assert.equal(view.expected, 50);
});

test("additive rich metadata stays irrelevant to stable auto and weekly meter selection", () => {
  const source = { ...reading({ ...meter, id: "auto", measurement: "percent", utilization: 25 }).providers[0],
    usage_details: { finances: { items: [{ prepaid_remaining_minor: 999999 }] }, reset_inventory: "malformed" },
    future: ["ignored"], buckets: [{ ...meter, id: "auto", measurement: "percent", utilization: 25 },
      { ...meter, id: "weekly", measurement: "percent", utilization: 40 }] };
  const snapshot: Snapshot = { status: "ready", providers: decodeUsage(JSON.stringify([source])) };
  assert.equal(keyView(snapshot, { provider: "Cursor", bucket: "auto" }, now).value, "25%");
  assert.equal(keyView(snapshot, { provider: "Cursor", bucket: "weekly" }, now).value, "40%");
  assert.equal(keyView(snapshot, { provider: "Cursor", bucket: "wallet" }, now).used, undefined);
  assert.throws(() => decodeUsage("not json"));
});

test("inherited legacy billing status stays status-only without overriding explicit percent", () => {
  const source = { ...reading(meter).providers[0], primary_status_text: "Enabled",
    buckets: [{ ...meter, status_text: null }] };
  const legacy: Snapshot = { status: "ready", providers: decodeUsage(JSON.stringify([source])) };
  assert.equal(keyView(legacy, { provider: "Cursor" }, now).used, undefined);
  const measured = { ...source, buckets: [{ ...meter, status_text: null, measurement: "percent" }] };
  const snapshot: Snapshot = { status: "ready", providers: decodeUsage(JSON.stringify([measured])) };
  assert.equal(keyView(snapshot, { provider: "Cursor" }, now).used, 0);
});
