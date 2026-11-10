import assert from "node:assert/strict";
import { test } from "node:test";
import { decodeUsage, keyView, presentKey, type Provider, type Snapshot } from "../src/model.js";
import type { WarningState } from "../src/pacing.js";

const now = Date.parse("2026-10-13T12:00:00Z"), day = 24 * 3600 * 1000;
const provider: Provider = {
  provider_name: "Claude", error: null, is_success: true, needs_reauth: false,
  auth: { state: "signed_in", source: { kind: "cli", name: "Claude Code" } },
  fetch_status: { credential_epoch: "account", fetched_at: new Date(now).toISOString() },
  buckets: [{ id: "session", label: "Session", utilization: 35, resets_at: "2026-10-13T14:30:00Z" }],
};
const snapshot: Snapshot = { status: "ready", providers: [provider] };
const failed = (patch: Partial<Provider> = {}): Snapshot => ({ status: "ready", providers: [{
  ...provider, error: "secret provider error", is_success: false, buckets: [],
  fetch_status: { failure_kind: "rate_limited", credential_epoch: "account" }, ...patch,
}] });

for (const failure_kind of ["rate_limited", "transient"]) {
  test(`${failure_kind} preserves a muted, frozen last reading through the 24-hour boundary`, () => {
    const warning: WarningState = { level: 0 };
    const live = presentKey(snapshot, {}, now, warning);
    const error: Snapshot = { status: "ready", providers: decodeUsage(JSON.stringify(failed({
      fetch_status: { failure_kind, credential_epoch: "account" },
    }).providers)) };
    for (const at of [now + 1000, now + 3600000, now + day]) {
      const result = presentKey(error, {}, at, warning, live.retained);
      assert.equal(result.view.used, live.view.used);
      assert.equal(result.view.expected, live.view.expected);
      assert.equal(result.view.pace, live.view.pace);
      assert.equal(result.view.detail, live.view.detail);
      assert.equal(result.view.value, live.view.value);
      assert.equal(result.view.color, "#6272a4");
      assert.equal(result.view.stale, true);
      assert.equal(warning.level, 0);
      assert.doesNotMatch(JSON.stringify(result.view), /secret provider error/);
    }
    assert.equal(keyView(error, {}, now).used, undefined);
    assert.equal(presentKey(error, {}, now + day + 1000, warning, live.retained).view.used, undefined);
  });
}

test("failure gates hide usage and identity-breaking failures cannot resurrect an old reading", () => {
  const live = presentKey(snapshot, {}, now);
  const cases: { readonly name: string; readonly patch: Partial<Provider>; readonly resurrect: boolean }[] = [
    { name: "other", patch: { fetch_status: { failure_kind: "other", credential_epoch: "account" } }, resurrect: true },
    { name: "missing kind", patch: { fetch_status: { credential_epoch: "account" } }, resurrect: true },
    { name: "auth", patch: { fetch_status: { failure_kind: "auth", credential_epoch: "account" } }, resurrect: false },
    { name: "reauth", patch: { needs_reauth: true }, resurrect: false },
    { name: "expired", patch: { auth: { state: "expired" } }, resurrect: false },
    { name: "signed out", patch: { auth: { state: "signed_out" } }, resurrect: false },
    { name: "unknown auth", patch: { auth: { state: "unknown" } }, resurrect: true },
    { name: "new epoch", patch: { fetch_status: { failure_kind: "rate_limited", credential_epoch: "other" } }, resurrect: false },
    { name: "empty epoch", patch: { fetch_status: { failure_kind: "rate_limited", credential_epoch: "" } }, resurrect: false },
    { name: "missing epoch", patch: { fetch_status: { failure_kind: "rate_limited" } }, resurrect: false },
    { name: "missing metadata", patch: { fetch_status: undefined }, resurrect: false },
    { name: "source kind", patch: { auth: { state: "signed_in", source: { kind: "browser", name: "Claude Code" } } }, resurrect: false },
    { name: "source name", patch: { auth: { state: "signed_in", source: { kind: "cli", name: "OpenCode" } } }, resurrect: false },
    { name: "provider identity", patch: { provider_name: "Claude (Renamed)", provider_type: "Claude" }, resurrect: false },
  ];
  for (const { name, patch, resurrect } of cases) {
    const result = presentKey(failed(patch), {}, now + 1000, undefined, live.retained);
    assert.equal(result.view.used, undefined, name);
    assert.doesNotMatch(JSON.stringify(result.view), /secret provider error/, name);
    const later = presentKey(failed(), {}, now + 2000, undefined, result.retained);
    assert.equal(later.view.used, resurrect ? 35 : undefined, name);
  }
});

test("older auth metadata and absent sources retain compatibility while a new source retires it", () => {
  const live = presentKey(snapshot, {}, now);
  for (const auth of [undefined, { state: "signed_in" }, { state: "signed_in", source: null },
    { state: "signed_in", source: {} }]) {
    assert.equal(presentKey(failed({ auth }), {}, now, undefined, live.retained).view.used, 35);
  }
  const withoutSource = presentKey({ ...snapshot, providers: [{ ...provider, auth: { state: "signed_in" } }] }, {}, now);
  assert.equal(presentKey(failed(), {}, now, undefined, withoutSource.retained).view.used, undefined);
});

test("offline, missing and invalid snapshots hide meters without consuming the retained reading", () => {
  const live = presentKey(snapshot, {}, now);
  for (const [status, value] of [["offline", "Offline"], ["missing", "Setup"], ["invalid", "No data"]] as const) {
    const hidden = presentKey({ status, providers: [] }, {}, now, undefined, live.retained);
    assert.equal(hidden.view.value, value);
    assert.equal(hidden.view.used, undefined);
    assert.equal(presentKey(failed(), {}, now, undefined, hidden.retained).view.used, 35);
  }
});

test("missing provider or account rows discard retained usage on an accepted ready snapshot", () => {
  for (const account of ["", "work"]) {
    const row = account ? { ...provider, provider_name: "Claude (Work)", provider_type: "Claude",
      account: { id: account, label: "Work" } } : provider;
    const live = presentKey({ ...snapshot, providers: [row] }, { account }, now);
    assert.equal(live.view.used, 35);
    const removed = presentKey({ status: "ready", providers: account ? [provider] : [] }, { account }, now, undefined, live.retained);
    assert.equal(removed.view.value, "Disabled");
    assert.equal(presentKey(failed({ ...row, error: "secret provider error", is_success: false, buckets: [],
      fetch_status: { failure_kind: "rate_limited", credential_epoch: "account" } }),
    { account }, now, undefined, removed.retained).view.used, undefined);
  }
});

test("no success, no selected meter and an empty success epoch cannot manufacture stale usage", () => {
  assert.equal(presentKey(failed(), {}, now).view.used, undefined);
  const live = presentKey(snapshot, {}, now);
  for (const buckets of [[], [{ ...provider.buckets[0], measurement: "status" }]]) {
    const empty = presentKey({ ...snapshot, providers: [{ ...provider, buckets }] }, {}, now, undefined, live.retained);
    assert.equal(empty.view.value, "No meter");
    assert.equal(presentKey(failed(), {}, now, undefined, empty.retained).view.used, undefined);
  }
  const missing = presentKey(snapshot, { bucket: "weekly" }, now);
  assert.equal(presentKey(failed(), { bucket: "weekly" }, now, undefined, missing.retained).view.used, undefined);
  const emptyEpoch = presentKey({ ...snapshot, providers: [{ ...provider, fetch_status: {} }] }, {}, now);
  assert.equal(presentKey(failed(), {}, now, undefined, emptyEpoch.retained).view.used, undefined);
});

test("a reading older than a day is not reused; missing or future fetched_at still can be", () => {
  const overlay = (fetched_at: string | null | undefined) => {
    const success: Snapshot = { ...snapshot, providers: [{ ...provider,
      fetch_status: { credential_epoch: "account", fetched_at } }] };
    return presentKey(failed(), {}, now, undefined, presentKey(success, {}, now).retained).view.used;
  };
  assert.equal(overlay(new Date(now - day - 1000).toISOString()), undefined);
  assert.equal(overlay(new Date(now + day).toISOString()), 35);
  assert.equal(overlay("invalid"), 35);
  assert.equal(overlay(null), 35);
  assert.equal(overlay(undefined), 35);
});

test("a later successful zero reading restores live presentation and replaces the retained meter", () => {
  const live = presentKey(snapshot, { display: "remaining" }, now);
  const stale = presentKey(failed(), { display: "remaining" }, now + 1000, undefined, live.retained);
  assert.equal(stale.view.value, "65%");
  const recovered = presentKey({ ...snapshot, providers: [{ ...provider,
    buckets: [{ ...provider.buckets[0], utilization: 0 }] }] }, { display: "remaining" }, now + 3600000, undefined, stale.retained);
  assert.equal(recovered.view.used, 0);
  assert.equal(recovered.view.value, "100%");
  assert.equal(recovered.view.stale, undefined);
  assert.equal(recovered.view.color, "#bd93f9");
  assert.equal(recovered.view.expected, 70);
  assert.equal(recovered.view.detail, "Reset 1h 30m");
  assert.equal(presentKey(failed(), { display: "remaining" }, now + 3601000, undefined, recovered.retained).view.used, 0);
});
