import { build } from "esbuild";
import { Resvg } from "@resvg/resvg-js";
import { mkdir, readFile, writeFile, cp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = fileURLToPath(new URL("..", import.meta.url));
process.chdir(root);
const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--version")) throw new Error("Usage: npm run build -- --version SEMVER");
const version = args[1] ?? "0.0.0+dev";
const match = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.exec(version);
if (!match || match.slice(1, 4).some(n => Number(n) > 65534)) throw new Error("Expected stable Headroom SemVer, without v, with components <= 65534");
const out = path.join(root, "dist/com.aaronfeledy.headroom.sdPlugin");
await mkdir(path.join(out, "bin"), { recursive: true });
await cp("plugin", out, { recursive: true });
const manifest = JSON.parse(await readFile("plugin/manifest.json", "utf8"));
manifest.Version = match.slice(1, 4).join(".") + ".0";
await writeFile(path.join(out, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n");
// Preserve the full shared SemVer; Stream Deck requires a numeric four-part version.
await writeFile(path.join(out, "headroom-version.json"), JSON.stringify({ version }) + "\n");
await build({ entryPoints: ["src/plugin.ts"], outfile: path.join(out, "bin/plugin.js"), bundle: true,
  platform: "node", target: "node20", format: "cjs", legalComments: "eof" });
await cp("../../LICENSE", path.join(out, "LICENSE"));
await mkdir(path.join(out, "imgs"), { recursive: true });
await mkdir(path.join(out, "imgs/providers"), { recursive: true });
for (const provider of ["claude", "codex", "cursor", "grok"]) {
  await cp("../shared/provider-icons/" + provider + ".png", path.join(out, "imgs/providers", provider + ".png"));
}
await cp("../shared/provider-icons/README.md", path.join(out, "PROVIDER-MARKS.md"));
const logo = await readFile("../desktop/headroom.svg", "utf8");
for (const [name, size] of [["plugin", 256], ["plugin@2x", 512], ["key", 72], ["key@2x", 144]]) {
  const png = new Resvg(logo, { fitTo: { mode: "width", value: size } }).render().asPng();
  await writeFile(path.join(out, "imgs", name + ".png"), png);
}
for (const [name, size] of [["action", 20], ["action@2x", 40], ["category", 28], ["category@2x", 56]]) {
  await writeFile(path.join(out, "imgs", name + ".svg"),
    '<svg xmlns="http://www.w3.org/2000/svg" width="' + size + '" height="' + size + '" viewBox="0 0 28 28"><path d="M6 23V5m16 18V5M6 14h16" fill="none" stroke="#fff" stroke-width="3" stroke-linecap="round"/></svg>');
}
const notices = [];
for (const name of ["@elgato/streamdeck", "@elgato/utils", "@elgato/schemas", "ws"]) {
  const folder = path.join("node_modules", name);
  const pkg = JSON.parse(await readFile(path.join(folder, "package.json"), "utf8"));
  const license = await readFile(path.join(folder, "LICENSE"), "utf8");
  notices.push(name + " " + pkg.version + "\n" + license);
}
await writeFile(path.join(out, "THIRD-PARTY-NOTICES.txt"), notices.join("\n\n"));
console.log("Built Headroom Stream Deck " + version);
