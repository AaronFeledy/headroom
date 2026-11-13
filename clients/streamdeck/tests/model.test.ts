import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { decodeUsage, keyView, providers, resetLabel, settings, type Provider, type Snapshot } from "../src/model.js";
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
test("OpenCode Go keys use the OpenCodeGo identifier, display name and stable meters", () => {
  const p = { ...provider, provider_name: "OpenCodeGo", provider_type: "OpenCodeGo", buckets: [
    { ...provider.buckets[0], id: "session", label: "5-Hour" },
    { ...provider.buckets[1], id: "weekly", utilization: 40, resets_at: null },
    { ...provider.buckets[1], id: "monthly", label: "Monthly", utilization: 55, resets_at: null }] };
  const ready: Snapshot = { status: "ready", providers: [p] };
  assert.equal(providers.OpenCodeGo, "OpenCode Go");
  assert.equal(settings({ provider: "OpenCodeGo" }).provider, "OpenCodeGo");
  assert.equal(keyView(ready, { provider: "OpenCodeGo" }, now).provider, "OpenCode Go");
  assert.equal(keyView(ready, { provider: "OpenCodeGo" }, now).label, "5-Hour");
  assert.equal(keyView(ready, { provider: "OpenCodeGo", bucket: "monthly" }, now).value, "55%");
  assert.equal(keyView(ready, { provider: "Codex" }, now).value, "Disabled");
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
test("blank account selects the default Claude row, not an extra account", () => {
  const bridge = JSON.stringify([
    {
      provider_name: "Claude", provider_type: "Claude", account: null,
      error: null, is_success: true, needs_reauth: false, auth: { state: "signed_in" },
      buckets: [{ id: "session", label: "Session", utilization: 20, resets_at: "2026-10-13T08:00:00Z" }],
    },
    {
      provider_name: "Claude (Work🏢)", provider_type: "Claude",
      account: { id: "work", label: "Work🏢" }, future_field: 1,
      error: null, is_success: true, needs_reauth: false, auth: { state: "signed_in" },
      buckets: [{ id: "session", label: "Session", utilization: 42, resets_at: "2026-10-13T08:00:00Z" }],
    },
  ]);
  const rows = decodeUsage(bridge);
  assert.equal(rows[1].account?.label, "Work🏢");
  assert.ok("future_field" in rows[1]);
  assert.equal(rows[1].future_field, 1);
  const ready: Snapshot = { status: "ready", providers: rows };
  const blank = keyView(ready, { provider: "Claude" }, now);
  assert.equal(blank.used, 20);
  assert.equal(blank.account, undefined);
  const work = keyView(ready, { provider: "Claude", account: "work" }, now);
  assert.equal(work.used, 42);
  assert.equal(work.account, "Work🏢");
  assert.equal(work.expected, 60);
  assert.equal(work.pace, "54m under pace");
  assert.ok(renderKey(work).includes("Work🏢"));
});
test("old-server rows without provider_type still match the default account", () => {
  const ready: Snapshot = {
    status: "ready",
    providers: [{
      provider_name: "Claude", error: null, is_success: true, needs_reauth: false, auth: { state: "signed_in" },
      buckets: [{ id: "session", label: "Session", utilization: 33, resets_at: "2026-10-13T08:00:00Z" }],
    }],
  };
  assert.equal(keyView(ready, { provider: "Claude" }, now).used, 33);
  assert.equal(keyView(ready, { provider: "Claude", account: "work" }, now).value, "Disabled");
});
test("invalid account settings normalize to blank and skip extra rows", () => {
  assert.equal(settings({ account: "Work" }).account, "");
  assert.equal(settings({ account: "work!" }).account, "");
  assert.equal(settings({ account: "" }).account, "");
  assert.equal(settings({ account: "work" }).account, "work");
  const ready: Snapshot = {
    status: "ready",
    providers: [{
      provider_name: "Claude (Work)", provider_type: "Claude", account: { id: "work", label: "Work" },
      error: null, is_success: true, needs_reauth: false, auth: { state: "signed_in" },
      buckets: [{ id: "session", label: "Session", utilization: 42, resets_at: "2026-10-13T08:00:00Z" }],
    }],
  };
  assert.equal(keyView(ready, { provider: "Claude", account: "Work" }, now).value, "Disabled");
});
test("decodeUsage follows the shared account identity fixture", () => {
  const fixture: {
    readonly account_ids: { readonly valid: readonly string[]; readonly invalid: readonly string[] };
    readonly account_labels: {
      readonly valid: readonly { readonly raw: string; readonly normalized: string }[];
      readonly invalid: readonly string[];
    };
    readonly provider_types: {
      readonly valid: readonly string[]; readonly invalid: readonly string[]; readonly invalid_values: readonly unknown[];
    };
    readonly account_values: { readonly null_results: readonly unknown[] };
  } = JSON.parse(readFileSync(new URL("../../../server/internal/usage/testdata/account_identity.json", import.meta.url), "utf8"));
  const decode = (metadata: { readonly account?: unknown; readonly provider_type?: unknown }): Provider => {
    const rows = decodeUsage(JSON.stringify([{ ...provider, provider_name: "Claude (Work)", ...metadata }]));
    assert.equal(rows.length, 1);
    return rows[0];
  };
  for (const id of fixture.account_ids.valid) {
    for (const label of fixture.account_labels.valid) {
      assert.deepEqual(decode({ account: { id, label: label.raw } }).account, { id, label: label.normalized });
    }
  }
  for (const id of fixture.account_ids.invalid) {
    assert.equal(decode({ account: { id, label: "Work" } }).account, null, JSON.stringify(id));
  }
  for (const label of fixture.account_labels.invalid) {
    assert.equal(decode({ account: { id: "work", label } }).account, null, JSON.stringify(label));
  }
  for (const account of fixture.account_values.null_results) {
    assert.equal(decode({ account }).account, null, JSON.stringify(account));
  }
  for (const provider_type of [...fixture.provider_types.valid, "\u{1f600}".repeat(32)]) {
    const row = decode({ provider_type });
    assert.equal(row.provider_type, provider_type);
  }
  for (const provider_type of [...fixture.provider_types.invalid, ...fixture.provider_types.invalid_values, "\u{1f600}".repeat(33)]) {
    const row = decode({ provider_type });
    assert.equal(row.provider_type, undefined);
  }
});
