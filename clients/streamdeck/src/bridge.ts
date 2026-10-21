import { execFile, spawn } from "node:child_process";
import { homedir } from "node:os";
import path from "node:path";
import { decodeUsage, type Snapshot } from "./model.js";

export function cliPath(custom = "", platform = process.platform, env = process.env, home = homedir()): string {
  const paths = platform === "win32" ? path.win32 : path.posix;
  if (custom) {
    if (!paths.isAbsolute(custom) || /[\r\n\0]/.test(custom) || platform === "win32" && !custom.toLowerCase().endsWith(".exe")) {
      throw new Error("Choose an absolute path to the Headroom CLI executable");
    }
    return custom;
  }
  if (platform === "win32") {
    if (!env.LOCALAPPDATA || !paths.isAbsolute(env.LOCALAPPDATA)) throw new Error("Headroom installation not found");
    return paths.join(env.LOCALAPPDATA, "Headroom", "cli", "headroom.exe");
  }
  return paths.join(home, ".local", "bin", "headroom");
}

// The stable CLI launcher establishes its own managed-install identity.
// Do not inherit credential, transport, or installer overrides from Stream Deck.
export function childEnvironment(env = process.env): NodeJS.ProcessEnv {
  return Object.fromEntries(Object.entries(env).filter(([name]) =>
    !/^(HEADROOM_|USAGE_|CLAUDE_|CODEX_|CURSOR_|GROK_|NODE_OPTIONS$|NODE_EXTRA_CA_CERTS$|NODE_TLS_REJECT_UNAUTHORIZED$)/i.test(name)));
}

export type Execute = (file: string, args: string[]) => Promise<{ stdout: string; stderr: string }>;
export const execute: Execute = (file, args) => new Promise((resolve, reject) => {
  const child = execFile(file, args, {
    shell: false, windowsHide: true, timeout: 15000, maxBuffer: 1024 * 1024,
    encoding: "utf8", env: childEnvironment(),
  }, (error, stdout, stderr) => error ? reject(error) : resolve({ stdout, stderr }));
  child.stdin?.end();
});

export async function readUsage(custom = "", run: Execute = execute): Promise<Snapshot> {
  let file: string;
  try { file = cliPath(custom); } catch { return { providers: [], status: "missing" }; }
  let output;
  try { output = await run(file, ["--json", "--once"]); }
  catch (error) {
    return { providers: [], status: (error as NodeJS.ErrnoException).code === "ENOENT" ? "missing" : "offline" };
  }
  // A stale desktop snapshot remains a JSON array; its offline status is on stderr.
  // Never show that cached body as current usage, or log account output.
  if (output.stderr.trim()) return { providers: [], status: "offline" };
  try { return { providers: decodeUsage(output.stdout), status: "ready" }; }
  catch { return { providers: [], status: "invalid" }; }
}

// The GUI inherits no captured pipes, so opening a new desktop can return as soon
// as the stable launcher exits rather than waiting for the GUI to close.
export const launchDesktop: Execute = (file, args) => new Promise((resolve, reject) => {
  const child = spawn(file, args, { shell: false, windowsHide: true, stdio: "ignore",
    timeout: 15000, env: childEnvironment() });
  child.once("error", reject);
  child.once("exit", code => code === 0 ? resolve({ stdout: "", stderr: "" }) : reject(new Error("Desktop launch failed")));
});

export async function openHeadroom(custom = "", run: Execute = launchDesktop): Promise<void> {
  await run(cliPath(custom), ["desktop"]);
}
