import type { WarningLevel } from "./pacing.js";

export const PULSE_DURATION_MS = 900;
export const PULSE_FRAME_MS = 50;

// Track raw utilization, not its rounded percentage or the time-based pace.
// A first reading, reset, account change, or recovery establishes a new baseline.
export class UsageActivity {
  private previous?: { used: number; window: string };
  private started?: number;

  observe(used: number | undefined, window: string | undefined, now: number): void {
    const old = this.previous;
    if (used === undefined || window === undefined) {
      this.previous = undefined;
      this.started = undefined;
      return;
    }
    this.previous = { used, window };
    if (!old || old.window !== window || used < old.used) {
      this.started = undefined;
    } else if (used > old.used + 1e-9) {
      this.started = now;
    }
  }

  strength(now: number): number {
    if (this.started === undefined) return 0;
    const elapsed = now - this.started;
    if (elapsed < 0 || elapsed >= PULSE_DURATION_MS) {
      this.started = undefined;
      return 0;
    }
    // One gentle swell, with a quicker rise and a longer fade.
    const peak = 150;
    return elapsed < peak ? Math.sin(elapsed / peak * Math.PI / 2)
      : Math.cos((elapsed - peak) / (PULSE_DURATION_MS - peak) * Math.PI / 2) ** 2;
  }

  stop(): void { this.started = undefined; }

  get active(): boolean { return this.started !== undefined; }
}

export const CRITICAL_BEAT_MS = 800;
export const CRITICAL_DURATION_MS = 3 * CRITICAL_BEAT_MS;

// Alert only on an observed transition in the same meter/account/window.
// The warning model's hysteresis determines when the alert can rearm.
export class CriticalActivity {
  private previous?: { level: WarningLevel; window: string };
  private started?: number;

  observe(level: WarningLevel, window: string | undefined, now: number): void {
    const old = this.previous;
    if (window === undefined) {
      this.previous = undefined;
      this.started = undefined;
      return;
    }
    this.previous = { level, window };
    if (!old || old.window !== window || level !== 3) {
      this.started = undefined;
    } else if (old.level !== 3) {
      this.started = now;
    }
  }

  strength(now: number): number {
    if (this.started === undefined) return 0;
    const elapsed = now - this.started;
    if (elapsed < 0 || elapsed >= CRITICAL_DURATION_MS) {
      this.started = undefined;
      return 0;
    }
    // Three distinct beats: a quick swell, a long fade, then a short pause.
    const phase = elapsed % CRITICAL_BEAT_MS;
    if (phase >= 700) return 0;
    return phase < 150 ? Math.sin(phase / 150 * Math.PI / 2)
      : Math.cos((phase - 150) / 550 * Math.PI / 2) ** 2;
  }

  get active(): boolean { return this.started !== undefined; }
}
