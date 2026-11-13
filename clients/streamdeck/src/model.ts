import { pacing, statusOnly, warningColors, warningLevel, type WarningState } from "./pacing.js";

export const providers = { Claude: "Claude", Codex: "ChatGPT", Cursor: "Cursor", Grok: "Grok", OpenCodeGo: "OpenCode Go" } as const;
export type ProviderID = keyof typeof providers;
export type Settings = { provider?: string; account?: string; bucket?: string; display?: string; cliPath?: string };
export type Bucket = { id: string; label: string; utilization: number; resets_at: string | null; starts_at?: unknown; status_text?: unknown; measurement?: unknown };
export type ProviderAccount = { id: string; label: string };
export type Provider = {
  provider_name: string;
  provider_type?: string;
  account?: ProviderAccount | null;
  error: string | null;
  is_success: boolean;
  needs_reauth: boolean;
  auth?: { state: string; source?: { readonly kind?: string; readonly name?: string } | null };
  primary_status_text?: unknown;
  secondary_status_text?: unknown;
  buckets: Bucket[];
  fetch_status?: { failure_kind?: string | null; credential_epoch?: string | null; fetched_at?: string | null };
};
export type Snapshot = { providers: Provider[]; status: "ready" | "offline" | "missing" | "invalid" };
export type KeyView = { provider: string; account?: string; label: string; value: string; detail: string; used?: number;
  expected?: number; pace?: string; color?: string; display?: "used" | "left"; stale?: boolean };
export type RetainedReading = {
  readonly view: Readonly<KeyView>;
  readonly epoch: string;
  readonly source: string;
  readonly observedAt: number;
  readonly provider_name: string;
};

const accountID = /^[a-z0-9][a-z0-9_-]{0,31}$/;

function providerType(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" && [...value].length <= 32 ? value : undefined;
}

function providerAccount(value: unknown): ProviderAccount | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  if (!("id" in value) || typeof value.id !== "string" || !accountID.test(value.id) ||
      !("label" in value) || typeof value.label !== "string") return null;
  const label = value.label.trim();
  const scalars = [...label];
  if (scalars.length < 1 || scalars.length > 32 || /\p{Cc}/u.test(label)) return null;
  return { id: value.id, label };
}

export function settings(value: Settings): Required<Settings> {
  return {
    provider: Object.hasOwn(providers, value.provider ?? "") ? value.provider! : "Claude",
    account: typeof value.account === "string" && accountID.test(value.account) ? value.account : "",
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
    const type = providerType(p.provider_type);
    if (type !== undefined) p.provider_type = type;
    else delete p.provider_type;
    if ("account" in p) {
      p.account = providerAccount(p.account);
    }
    const ids = new Set<string>();
    for (const [index, b] of p.buckets.entries()) {
      if (!b || typeof b.id !== "string" || !b.id || ids.has(b.id) || typeof b.label !== "string" ||
          typeof b.utilization !== "number" || !Number.isFinite(b.utilization) || b.utilization < 0 || b.utilization > 100 ||
          !(b.resets_at === null || typeof b.resets_at === "string" && Number.isFinite(Date.parse(b.resets_at)))) {
        throw new Error("Invalid meter");
      }
      ids.add(b.id);
      if (typeof b.status_text !== "string" || b.status_text.trim() === "") {
        const id = b.id.toLowerCase();
        if ((id === "api" || index === 0 && id !== "auto") && typeof p.primary_status_text === "string") b.status_text = p.primary_status_text;
        else if (id === "weekly" || id === "on_demand") b.status_text = p.secondary_status_text;
      }
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

function matchingProvider(snapshot: Snapshot, config: Required<Settings>): Provider | undefined {
  return snapshot.providers.find((row) =>
    (row.provider_type || row.provider_name) === config.provider && (row.account?.id ?? "") === config.account);
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
  const p = matchingProvider(snapshot, config);
  if (!p) return unavailable("Disabled", "Enable in app");
  if (p.needs_reauth || ["signed_out", "expired"].includes(p.auth?.state ?? "")) return unavailable("Sign in");
  if (p.error !== null || !p.is_success) return unavailable("No data");
  if (p.fetch_status?.failure_kind) return unavailable("Retrying", "Reading unavailable");
  // A specific missing meter never falls back to a different meter.
  const b = config.bucket === "first" ? p.buckets.find(b => !statusOnly(b)) : p.buckets.find((b) => b.id === config.bucket);
  if (!b) return unavailable("No meter", "Choose a meter");
  if (statusOnly(b)) return unavailable("No meter", "Billing status only");
  const type = p.provider_type || p.provider_name;
  const pace = pacing(type, b, now);
  const window = JSON.stringify([p.provider_name, b.id, b.starts_at, b.resets_at, p.fetch_status?.credential_epoch]);
  const level = warningLevel(b.utilization, pace, warning?.window === window ? warning.level : 0);
  if (warning) { warning.window = window; warning.level = level; }
  const amount = config.display === "remaining" ? 100 - b.utilization : b.utilization;
  return {
    provider, ...(p.account?.label ? { account: p.account.label } : {}), label: b.label,
    display: config.display === "remaining" ? "left" : "used",
    value: Math.round(amount) + "%", detail: resetLabel(b.resets_at, now), used: b.utilization,
    expected: pace.expected, pace: pace.label, color: warningColors[level],
  };
}

// Capture the already-computed live presentation so clocks cannot advance stale pace or reset text.
function retainReading(previous: RetainedReading | undefined, provider: Provider | undefined,
  view: KeyView, now: number): RetainedReading | undefined {
  if (!provider) return undefined;
  const authSource = provider.auth?.source;
  const source = authSource && Object.keys(authSource).length ? (authSource.kind ?? "") + "\n" + (authSource.name ?? "") : "";
  const epoch = provider.fetch_status?.credential_epoch ?? "";
  if (provider.is_success) {
    if (view.used === undefined) return undefined;
    const fetchedAt = Date.parse(provider.fetch_status?.fetched_at ?? "");
    return { view: { ...view }, epoch, source, provider_name: provider.provider_name,
      observedAt: Number.isFinite(fetchedAt) ? Math.min(fetchedAt, now) : now };
  }
  if (!previous || provider.provider_name !== previous.provider_name || provider.needs_reauth ||
      provider.fetch_status?.failure_kind === "auth" || provider.auth?.state === "expired" ||
      provider.auth?.state === "signed_out" || !epoch || epoch !== previous.epoch ||
      source !== "" && source !== previous.source) return undefined;
  return previous;
}

export function presentKey(snapshot: Snapshot | undefined, raw: Settings, now = Date.now(),
  warning?: WarningState, previous?: RetainedReading): { readonly view: KeyView; readonly retained: RetainedReading | undefined } {
  const view = keyView(snapshot, raw, now, warning);
  if (snapshot?.status !== "ready") return { view, retained: previous };
  const provider = matchingProvider(snapshot, settings(raw));
  const retained = retainReading(previous, provider, view, now);
  const kind = provider?.fetch_status?.failure_kind;
  const epoch = provider?.fetch_status?.credential_epoch;
  const signedIn = provider && !provider.needs_reauth && (provider.auth === undefined || provider.auth.state === "signed_in");
  if (provider && !provider.is_success && retained?.view.used !== undefined &&
      now - retained.observedAt <= 24 * 3600 * 1000 && signedIn &&
      (kind === "transient" || kind === "rate_limited") && epoch && epoch === retained.epoch) {
    return { view: { ...retained.view, color: "#6272a4", stale: true }, retained };
  }
  return { view, retained };
}
