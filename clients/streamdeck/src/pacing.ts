import type { Bucket } from "./model.js";

export type WarningLevel = 0 | 1 | 2 | 3;
export type Pace = { expected?: number; label: string; pressure: number };
export const warningColors = ["#bd93f9", "#f1fa8c", "#ffb86c", "#ff5555"] as const;
export type WarningState = { window?: string; level: WarningLevel };

// Keep in step with clients/desktop/usage.cpp (period/pacing/concern) and
// clients/desktop/warning.cpp. These are estimates, never a run-out prediction.
const DAY = 86400;
function timestamp(value: unknown): number {
  return typeof value === "string" ? Date.parse(value) : NaN;
}
export function statusOnly(bucket: Bucket): boolean {
  if (bucket.measurement !== undefined && bucket.measurement !== null) return bucket.measurement !== "percent";
  return bucket.id.toLowerCase() === "on_demand" && bucket.utilization <= 0 &&
    typeof bucket.status_text === "string" && bucket.status_text.length > 0 && !bucket.status_text.includes(" / ");
}

export function windowSeconds(provider: string, bucket: Bucket): number {
  const name = provider.toLowerCase(), id = bucket.id.toLowerCase();
  const reset = timestamp(bucket.resets_at), start = timestamp(bucket.starts_at);
  const reported = Math.trunc((reset - start) / 1000);
  if (reported > 0 && reported <= 366 * DAY) return reported;
  const known = ["claude", "codex", "cursor", "grok"].includes(name);
  if (known && (id === "weekly" || id.startsWith("weekly_"))) return 7 * DAY;
  if (["claude", "codex"].includes(name) && id === "session") return /weekly/i.test(bucket.label) ? 7 * DAY : 5 * 3600;
  if (name === "cursor" && ["session", "plan", "auto", "api", "on_demand"].includes(id)) return 30 * DAY;
  if (name === "grok" && ["session", "credits", "plan", "on_demand"].includes(id) && Number.isFinite(reset)) {
    // UTC calendar subtraction clamps March 31 to February 28/29, just like Qt.
    const end = new Date(reset), prior = new Date(reset);
    prior.setUTCDate(1);
    prior.setUTCMonth(prior.getUTCMonth() - 1);
    const lastDay = new Date(reset);
    lastDay.setUTCDate(0);
    prior.setUTCDate(Math.min(end.getUTCDate(), lastDay.getUTCDate()));
    return Math.trunc((reset - prior.getTime()) / 1000);
  }
  return 0;
}

function offsetLabel(seconds: number): string {
  const minutes = Math.floor((Math.abs(seconds) + 30) / 60);
  if (minutes >= 1440) {
    const hours = Math.floor(minutes % 1440 / 60);
    return Math.floor(minutes / 1440) + "d" + (hours ? " " + hours + "h" : "");
  }
  if (minutes >= 60) return Math.floor(minutes / 60) + "h" + (minutes % 60 ? " " + minutes % 60 + "m" : "");
  return minutes + "m";
}

export function pacing(provider: string, bucket: Bucket, now: number): Pace {
  const unknown: Pace = { label: "Pace unavailable", pressure: 0 };
  const reset = timestamp(bucket.resets_at);
  const duration = windowSeconds(provider, bucket);
  if (!Number.isFinite(reset) || reset <= now || duration <= 0 || statusOnly(bucket)) return unknown;
  const remaining = Math.trunc((reset - now) / 1000);
  if (remaining > duration) return unknown;
  const expected = 100 * (duration - remaining) / duration;
  const difference = bucket.utilization - expected;
  // Qt qRound64 rounds half away from zero.
  const rawSeconds = difference * duration / 100;
  const seconds = Math.sign(rawSeconds) * Math.round(Math.abs(rawSeconds));
  const label = Math.abs(seconds) < 60 ? "On pace" : offsetLabel(seconds) + (difference > 0 ? " over pace" : " under pace");
  const timeRemaining = 100 - expected;
  const pressure = Math.max(0, Math.min(1, (timeRemaining - (100 - bucket.utilization)) / Math.max(0.000001, timeRemaining)));
  return { expected, label, pressure };
}

export function warningLevel(used: number, pace: Pace, previous: WarningLevel = 0): WarningLevel {
  const enterPressure = [0, .10, .25, .50], exitPressure = [0, .08, .20, .40];
  const enterCapacity = [0, 95, 99, 100], exitCapacity = [0, 94, 98, 99.5];
  const enterFallback = [0, 50, 75, 90], exitFallback = [0, 45, 70, 85];
  let result: WarningLevel = 0;
  for (const tier of [1, 2, 3] as const) {
    const recovering = previous >= tier;
    const reached = pace.expected !== undefined
      ? pace.pressure >= (recovering ? exitPressure[tier] : enterPressure[tier]) - 1e-9 ||
        used >= (recovering ? exitCapacity[tier] : enterCapacity[tier])
      : used >= (recovering ? exitFallback[tier] : enterFallback[tier]);
    if (reached) result = tier;
  }
  return result;
}
