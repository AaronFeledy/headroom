import assert from "node:assert/strict";
import { test } from "node:test";
import { spawn } from "node:child_process";
import { cp, mkdtemp, readFile, writeFile, rm, readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { once } from "node:events";
import { setTimeout as delay } from "node:timers/promises";
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
// Exercise the shipped SDK bundle over a real socket with disposable data only.
const { WebSocketServer } = require("ws");

test("built plugin registers, paints keys, handles settings and opens the desktop", { timeout: 50000 }, async (t) => {
  const temporary = await mkdtemp(path.join(tmpdir(), "headroom-streamdeck-smoke-"));

  const plugin = path.join(temporary, "com.aaronfeledy.headroom.sdPlugin");
  await cp("dist/com.aaronfeledy.headroom.sdPlugin", plugin, { recursive: true });
  const server = new WebSocketServer({ host: "127.0.0.1", port: 0 });
  await once(server, "listening");
  t.after(() => { for (const c of server.clients) c.terminate(); server.close(); });
  const usage = JSON.stringify([{ provider_name: "Codex", error: null, is_success: true, needs_reauth: false,
    buckets: [{ id: "weekly", label: "Weekly", utilization: 37, resets_at: null }] }]);
  const cli = path.join(temporary, "fake headroom");
  const record = path.join(temporary, "opened");
  const usageFile = path.join(temporary, "usage.json");
  await writeFile(usageFile, usage);
  if (process.platform !== "win32") {
    const source = "#!" + process.execPath + "\n" +
      "const fs = require('node:fs'); if(process.argv[2] === 'desktop') fs.writeFileSync(" +
      JSON.stringify(record) + ", 'opened'); else process.stdout.write(fs.readFileSync(" + JSON.stringify(usageFile) + "));\n";
    await writeFile(cli, source, { mode: 0o700 });
  }
  const connected = once(server, "connection");
  const child = spawn(process.execPath, ["bin/plugin.js", "-port", String(server.address().port),
    "-pluginUUID", "fixture-plugin", "-registerEvent", "registerPlugin", "-info", JSON.stringify({
      application: { version: "7.1.0", platform: process.platform === "win32" ? "windows" : "mac", language: "en" },
      plugin: { uuid: "com.aaronfeledy.headroom", version: "0.0.0.0" }, devices: [{ id: "fixture", name: "Test Deck", type: 0, size: { columns: 5, rows: 3 } }]
    })], { cwd: plugin, stdio: "pipe" });
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) {
      const exited = once(child, "exit");
      child.kill();
      await exited;
    }
    await rm(temporary, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
  });
  let diagnostics = "";
  child.stderr.on("data", value => { diagnostics += value; });
  const [socket] = await Promise.race([connected, delay(3000).then(async () => {
    let logs = "";
    try { for (const name of await readdir(path.join(plugin, "logs"))) logs += await readFile(path.join(plugin, "logs", name), "utf8"); } catch {}
    throw new Error("Plugin did not connect: " + diagnostics + logs);
  })]);
  const messages: any[] = [];
  socket.on("message", (data: Buffer) => messages.push(JSON.parse(data.toString())));
  async function until(predicate: () => boolean, waitMs = 5000) {
    for (let i = 0; i < waitMs / 25 && !predicate(); i++) await delay(25);
    assert.ok(predicate(), "Expected Stream Deck event. " + diagnostics);
  }
  await until(() => messages.some(m => m.event === "registerPlugin"));
  const settings = { provider: "Codex", bucket: "weekly", cliPath: cli };
  function send(event: string, config = settings, action = "usage") {
    socket.send(JSON.stringify({ event, action: "com.aaronfeledy.headroom." + action, context: "key", device: "fixture",
      payload: { settings: config, controller: "Keypad", coordinates: { column: 0, row: 0 }, state: 0 } }));
  }
  const images = () => messages.filter(m => m.event === "setImage").map(m =>
    Buffer.from(m.payload.image.split(",")[1], "base64").toString());
  send("willAppear");
  if (process.platform !== "win32") {
    await until(() => images().some(s => s.includes("37%") && s.includes("ChatGPT")));
    assert.ok(!images().some(s => s.includes('data-role="activity-pulse"')));
    const increased = JSON.parse(usage);
    increased[0].buckets[0].utilization = 37.1;
    await writeFile(usageFile, JSON.stringify(increased));
    await until(() => images().some(s => s.includes('data-role="activity-pulse"')), 20000);
    await until(() => !images().at(-1)!.includes('data-role="activity-pulse"'));
    increased[0].buckets[0].utilization = 91;
    await writeFile(usageFile, JSON.stringify(increased));
    await until(() => images().some(s => s.includes('data-role="critical-pulse"')), 20000);
    const firstCriticalFrame = images().findIndex(s => s.includes('data-role="critical-pulse"'));
    await until(() => images().slice(firstCriticalFrame).filter(s => s.includes('data-role="critical-pulse"')).length >= 35);
    await until(() => !images().at(-1)!.includes('data-role="critical-pulse"'));
    assert.ok(images().slice(firstCriticalFrame).every(s => !s.includes('data-role="activity-pulse"')));
    assert.ok(images().at(-1)!.includes('stroke="#ff5555"'));
    send("keyDown");
    await until(() => { return messages.length > 0; });
    for (let i = 0; i < 100; i++) {
      try { if (await readFile(record, "utf8") === "opened") break; } catch {}
      await delay(25);
    }
    assert.equal(await readFile(record, "utf8"), "opened");
    send("didReceiveSettings", { ...settings, bucket: "missing" });
    await until(() => images().some(s => s.includes("No meter")));
  } else {
    await until(() => images().some(s => s.includes("Setup")));
  }
  const missing = { ...settings, cliPath: process.platform === "win32" ? "C:\\missing\\headroom.exe" : "/missing/headroom" };
  send("didReceiveSettings", missing);
  await until(() => images().some(s => s.includes("Setup")));
  send("keyDown", missing);
  await until(() => messages.some(m => m.event === "showAlert"));
  send("willDisappear");
  messages.length = 0;
  send("willAppear", missing, "open");
  send("keyDown", missing, "open");
  await until(() => messages.some(m => m.event === "showAlert"));
  assert.ok(!diagnostics.includes(usage));
});
