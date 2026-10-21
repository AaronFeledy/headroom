import assert from "node:assert/strict";
import { test } from "node:test";
import { childEnvironment, cliPath, execute, openHeadroom, readUsage, type Execute } from "../src/bridge.js";
const usage = JSON.stringify([{ provider_name: "Claude", error: null, is_success: true, needs_reauth: false,
  buckets: [{ id: "session", label: "Session", utilization: 50, resets_at: null }] }]);

test("known stable launchers are resolved without PATH or a shell", () => {
  assert.equal(cliPath("", "win32", { LOCALAPPDATA: "C:\\Users\\Test\\AppData\\Local" }, ""),
    "C:\\Users\\Test\\AppData\\Local\\Headroom\\cli\\headroom.exe");
  assert.equal(cliPath("", "darwin", {}, "/Users/Test"), "/Users/Test/.local/bin/headroom");
  assert.throws(() => cliPath("headroom.exe", "win32"));
  assert.throws(() => cliPath("C:\\custom\\headroom.cmd", "win32"));
  assert.throws(() => cliPath("/tmp/headroom\nbad", "darwin"));
});
test("only fixed read and desktop commands are passed; paths remain literal", async () => {
  const file = process.platform === "win32" ? "C:\\custom & install\\headroom.exe" : "/tmp/custom & install/headroom";
  const calls: unknown[] = [];
  const run: Execute = async (path, args) => { calls.push([path, args]); return { stdout: usage, stderr: "" }; };
  assert.equal((await readUsage(file, run)).status, "ready");
  await openHeadroom(file, run);
  assert.deepEqual(calls, [[file, ["--json", "--once"]], [file, ["desktop"]]]);
});
test("errors and offline stderr discard old data; missing installs are distinguishable", async () => {
  assert.deepEqual(await readUsage("", async () => ({ stdout: usage, stderr: "Offline · last good readings" })),
    { status: "offline", providers: [] });
  assert.equal((await readUsage("", async () => { throw Object.assign(new Error("private"), { code: "ENOENT" }); })).status, "missing");
  assert.equal((await readUsage("", async () => { throw new Error("secret"); })).status, "offline");
  assert.equal((await readUsage("", async () => ({ stdout: "malformed", stderr: "" }))).status, "invalid");
  assert.equal((await readUsage("", async () => ({ stdout: usage, stderr: "" }))).status, "ready");
});
test("child environment removes inherited transport and identity overrides", () => {
  const env = childEnvironment({ PATH: "path", HOME: "home", APPDATA: "app", headroom_auth_token: "secret",
    USAGE_AUTH_TOKEN: "secret", HEADROOM_INSTALL_ROOT: "wrong", NODE_OPTIONS: "bad", SSH_AUTH_SOCK: "agent" });
  assert.deepEqual(env, { PATH: "path", HOME: "home", APPDATA: "app", SSH_AUTH_SOCK: "agent" });
});
test("real child execution bounds output and passes shell metacharacters literally", async () => {
  const output = await execute(process.execPath, ["-e", "process.stdout.write(process.argv[1])", "$(echo secret); & literal"]);
  assert.equal(output.stdout, "$(echo secret); & literal");
  await assert.rejects(execute(process.execPath, ["-e", "process.stdout.write('x'.repeat(2*1024*1024))"]));
});
