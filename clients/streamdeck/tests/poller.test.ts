import assert from "node:assert/strict";
import { test } from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import { UsagePoller } from "../src/poller.js";
import type { Snapshot } from "../src/model.js";
const ready: Snapshot = { status: "ready", providers: [] };
test("keys share requests, hide stops polling, and old results cannot replace new settings", async () => {
  const resolvers = new Map<string, (value: Snapshot) => void>();
  const calls: string[] = [];
  const poller = new UsagePoller((path) => {
    calls.push(path);
    return new Promise(resolve => resolvers.set(path, resolve));
  }, 1000);
  const seen: string[] = [];
  poller.subscribe("a", "first", () => seen.push("obsolete"));
  poller.subscribe("b", "first", () => seen.push("hidden"));
  await delay(0);
  assert.deepEqual(calls, ["first"]);
  poller.subscribe("a", "second", () => seen.push("current"));
  poller.unsubscribe("b");
  await delay(0);
  resolvers.get("first")!(ready);
  resolvers.get("second")!(ready);
  await delay(0);
  assert.deepEqual(seen, ["current"]);
  poller.unsubscribe("a");
});
test("polling recovers after failure and stops when the last key disappears", async () => {
  let reads = 0;
  const poller = new UsagePoller(async () => {
    if (++reads === 1) throw new Error("offline");
    return ready;
  }, 10);
  const seen: string[] = [];
  poller.subscribe("a", "", value => seen.push(value.status));
  await delay(55);
  poller.unsubscribe("a");
  assert.equal(seen[0], "offline");
  assert.ok(seen.includes("ready"));
  const stopped = reads;
  await delay(35);
  assert.equal(reads, stopped);
});
