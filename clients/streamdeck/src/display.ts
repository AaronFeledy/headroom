import { UsageActivity, CriticalActivity, PULSE_FRAME_MS } from "./activity.js";
import { keyView, type KeyView, type Settings, type Snapshot } from "./model.js";
import type { WarningState } from "./pacing.js";
import { keyImage } from "./render.js";

export type AnimationClock = {
  now(): number;
  later(callback: () => void, delay: number): () => void;
};
const clock: AnimationClock = {
  now: Date.now,
  later(callback, delay) {
    const timer = setTimeout(callback, delay);
    timer.unref();
    return () => clearTimeout(timer);
  },
};

// Keep only the latest frame while a write is pending.
export class UsageKeyDisplay {
  private activity = new UsageActivity();
  private critical = new CriticalActivity();
  private warning: WarningState = { level: 0 };
  private view: KeyView;
  private cancelFrame?: () => void;
  private disposed = false;
  private pending?: string;
  private lastSent?: string;
  private writing = false;

  constructor(private config: Settings, private icons: Record<string, string>,
    private write: (image: string) => Promise<void>, private time: AnimationClock = clock) {
    this.view = keyView(undefined, config);
    this.paint();
  }

  configure(config: Settings): void {
    this.cancelFrame?.();
    this.cancelFrame = undefined;
    this.config = config;
    this.activity = new UsageActivity();
    this.critical = new CriticalActivity();
    this.warning = { level: 0 };
    this.view = keyView(undefined, config);
    this.paint();
  }

  update(snapshot: Snapshot): void {
    if (this.disposed) return;
    const now = this.time.now();
    this.view = keyView(snapshot, this.config, now, this.warning);
    this.critical.observe(this.warning.level, this.warning.window, now);
    this.activity.observe(this.view.used, this.warning.window, now);
    if (this.critical.active) this.activity.stop();
    this.paint();
  }

  dispose(): void {
    this.disposed = true;
    this.cancelFrame?.();
    this.cancelFrame = undefined;
    this.pending = undefined;
  }

  private paint(): void {
    if (this.disposed) return;
    this.cancelFrame?.();
    this.cancelFrame = undefined;
    const now = this.time.now();
    const critical = this.critical.strength(now);
    const strength = this.activity.strength(now);
    this.pending = keyImage(this.view, this.icons[this.view.provider], strength, critical);
    void this.flush();
    if (this.activity.active || this.critical.active) {
      this.cancelFrame = this.time.later(() => {
        this.cancelFrame = undefined;
        this.paint();
      }, PULSE_FRAME_MS);
    }
  }

  private async flush(): Promise<void> {
    if (this.writing) return;
    this.writing = true;
    try {
      while (!this.disposed && this.pending !== undefined) {
        const image = this.pending;
        this.pending = undefined;
        if (image === this.lastSent) continue;
        try {
          await this.write(image);
          this.lastSent = image;
        } catch {
          this.lastSent = undefined;
        }
      }
    } finally {
      this.writing = false;
    }
  }
}
