import type { Snapshot } from "./model.js";

type Listener = { path: string; receive: (value: Snapshot) => void };
export class UsagePoller {
  private listeners = new Map<string, Listener>();
  private pending = new Map<string, Promise<Snapshot>>();
  private timer?: ReturnType<typeof setTimeout>;
  constructor(private read: (path: string) => Promise<Snapshot>, private interval = 15000) {}

  subscribe(id: string, path: string, receive: Listener["receive"]): void {
    const listener = { path, receive };
    this.listeners.set(id, listener);
    void this.deliver(id, listener);
    this.schedule();
  }
  unsubscribe(id: string): void {
    this.listeners.delete(id);
    if (!this.listeners.size) { clearTimeout(this.timer); this.timer = undefined; }
  }
  private async deliver(id: string, listener: Listener): Promise<void> {
    let request = this.pending.get(listener.path);
    if (!request) {
      request = Promise.resolve().then(() => this.read(listener.path)).catch((): Snapshot => ({ providers: [], status: "offline" }));
      this.pending.set(listener.path, request);
      void request.then(() => this.pending.delete(listener.path));
    }
    const value = await request;
    // Settings changes and disappearing keys invalidate old in-flight deliveries.
    if (this.listeners.get(id) === listener) listener.receive(value);
  }
  private schedule(): void {
    if (this.timer || !this.listeners.size) return;
    this.timer = setTimeout(async () => {
      this.timer = undefined;
      await Promise.all([...this.listeners].map(([id, listener]) => this.deliver(id, listener)));
      this.schedule();
    }, this.interval);
    this.timer.unref();
  }
}
