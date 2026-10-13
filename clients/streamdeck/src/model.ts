import { pacing, statusOnly, warningColors, warningLevel, type WarningState } from "./pacing.js";

export const providers = { Claude: "Claude", Codex: "ChatGPT", Cursor: "Cursor", Grok: "Grok" } as const;
export type ProviderID = keyof typeof providers;
export type Settings = { provider?: string; bucket?: string; display?: string; cliPath?: string };
export type Bucket = { id: string; label: string; utilization: number; resets_at: string | null; starts_at?: unknown; status_text?: unknown };
export type Provider = {
  provider_name: string;
  error: string | null;
  is_success: boolean;
  needs_reauth: boolean;
  auth?: { state: string };
  buckets: Bucket[];
  fetch_status?: { failure_kind?: string | null; credential_epoch?: string | null };
};
export type Snapshot = { providers: Provider[]; status: "ready" | "offline" | "missing" | "invalid" };
export type KeyView = { provider: string; label: string; value: string; detail: string; used?: number;
  expected?: number; pace?: string; color?: string; display?: "used" | "left" };

export function settings(value: Settings): Required<Settings> {
  return {
    provider: Object.hasOwn(providers, value.provider ?? "") ? value.provider! : "Claude",
    bucket: typeof value.bucket === "string" && /^[a-z0-9_]{1,64}$/.test(value.bucket) ? value.bucket : "first",
    display: value.display === "remaining" ? "remaining" : "used",
    cliPath: typeof value.cliPath === "string" ? value.cliPath.trim() : "",
  };
}

// The API calls ChatGPT Codex.
export function decodeUsage(text: string): Provider[] {
  const data: unknown = JSON.parse(text);
  if (!Array.isArray(data) || data.length > 64) throw new Error("Invalid usage collection");
  const names = new Set<string>();
  for (const p of data) {
    if (!p || typeof p !== "object" || typeof p.provider_name !== "string" ||
        names.has(p.provider_name) || !(p.error === null || typeof p.error === "string") ||
        typeof p.needs_reauth !== "boolean" || p.is_success !== (p.error === null) ||
        !Array.isArray(p.buckets) || p.buckets.length > 12) throw new Error("Invalid provider");
    names.add(p.provider_name);
    const ids = new Set<string>();
    for (const b of p.buckets) {
      if (!b || typeof b.id !== "string" || !b.id || ids.has(b.id) || typeof b.label !== "string" ||
          typeof b.utilization !== "number" || !Number.isFinite(b.utilization) || b.utilization < 0 || b.utilization > 100 ||
          !(b.resets_at === null || typeof b.resets_at === "string" && Number.isFinite(Date.parse(b.resets_at)))) {
        throw new Error("Invalid meter");
      }
      ids.add(b.id);
    }
  }
  return data;
}

export function resetLabel(reset: string | null, now: number): string {
  if (!reset) return "No reset time";
  const minutes = Math.ceil((Date.parse(reset) - now) / 60000);
  if (minutes <= 0) return "Reset due";
  if (minutes < 60) return "Reset " + minutes + "m";
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return "Reset " + hours + "h " + minutes % 60 + "m";
  return "Reset " + Math.floor(hours / 24) + "d " + hours % 24 + "h";
}

export function keyView(snapshot: Snapshot | undefined, raw: Settings, now = Date.now(), warning?: WarningState): KeyView {
  const config = settings(raw);
  const provider = providers[config.provider as ProviderID];
  const unavailable = (value: string, detail = "Open Headroom"): KeyView => {
    if (warning) { warning.window = undefined; warning.level = 0; }
    return { provider, label: "", value, detail };
  };
  if (!snapshot) return unavailable("Loading");
  if (snapshot.status === "missing") return unavailable("Setup", "Install Headroom");
  if (snapshot.status === "invalid") return unavailable("No data", "Check Headroom");
  if (snapshot.status !== "ready") return unavailable("Offline");
  const p = snapshot.providers.find((p) => p.provider_name === config.provider);
  if (!p) return unavailable("Disabled", "Enable in app");
  if (p.needs_reauth || ["signed_out", "expired"].includes(p.auth?.state ?? "")) return unavailable("Sign in");
  if (p.error !== null || !p.is_success) return unavailable("No data");
  if (p.fetch_status?.failure_kind) return unavailable("Retrying", "Reading unavailable");
  // A specific missing meter never falls back to a different meter.
  const b = config.bucket === "first" ? p.buckets.find(b => !statusOnly(b)) : p.buckets.find((b) => b.id === config.bucket);
  if (!b) return unavailable("No meter", "Choose a meter");
  if (statusOnly(b)) return unavailable("No meter", "Billing status only");
  const pace = pacing(p.provider_name, b, now);
  const window = JSON.stringify([p.provider_name, b.id, b.starts_at, b.resets_at, p.fetch_status?.credential_epoch]);
  const level = warningLevel(b.utilization, pace, warning?.window === window ? warning.level : 0);
  if (warning) { warning.window = window; warning.level = level; }
  const amount = config.display === "remaining" ? 100 - b.utilization : b.utilization;
  return { provider, label: b.label, display: config.display === "remaining" ? "left" : "used",
    value: Math.round(amount) + "%", detail: resetLabel(b.resets_at, now), used: b.utilization,
    expected: pace.expected, pace: pace.label, color: warningColors[level] };
}
