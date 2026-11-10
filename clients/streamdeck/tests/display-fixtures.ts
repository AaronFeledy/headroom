import type { AnimationClock } from "../src/display.js";
import type { Snapshot } from "../src/model.js";

export class FakeClock implements AnimationClock {
  value = Date.parse("2026-10-13T12:00:00Z");
  jobs = new Map<number, { at: number; callback: () => void }>();
  id = 0;
  now = () => this.value;
  later = (callback: () => void, delay: number) => {
    const id = ++this.id;
    this.jobs.set(id, { at: this.value + delay, callback });
    return () => { this.jobs.delete(id); };
  };
  advance(ms: number): void {
    const end = this.value + ms;
    while (true) {
      const next = [...this.jobs].sort((a, b) => a[1].at - b[1].at)[0];
      if (!next || next[1].at > end) break;
      this.value = next[1].at;
      this.jobs.delete(next[0]);
      next[1].callback();
    }
    this.value = end;
  }
}
export const flush = () => new Promise<void>(resolve => setImmediate(resolve));
export const svg = (image: string) => Buffer.from(image.split(",")[1], "base64").toString();
export const reading = (used: number, epoch = "account"): Snapshot => ({ status: "ready", providers: [
  { provider_name: "Claude", error: null, is_success: true, needs_reauth: false,
    fetch_status: { credential_epoch: epoch },
    buckets: [{ id: "session", label: "Session", utilization: used, resets_at: "2026-10-13T14:30:00Z" }] }] });
export const rateLimited = (): Snapshot => ({ status: "ready", providers: [
  { provider_name: "Claude", error: "private provider error", is_success: false, needs_reauth: false,
    fetch_status: { credential_epoch: "account", failure_kind: "rate_limited" }, buckets: [] }] });
