import streamDeck, { action, SingletonAction, type DidReceiveSettingsEvent, type KeyDownEvent,
  type WillAppearEvent, type WillDisappearEvent } from "@elgato/streamdeck";
import { openHeadroom, readUsage } from "./bridge.js";
import { settings, type Settings } from "./model.js";
import { UsagePoller } from "./poller.js";
import { loadProviderIcons } from "./icons.js";
import { UsageKeyDisplay } from "./display.js";

const icons = loadProviderIcons("imgs/providers");
const poller = new UsagePoller(readUsage);
const opening = new Set<string>();
async function open(ev: KeyDownEvent<Settings>): Promise<void> {
  const path = settings(ev.payload.settings).cliPath;
  if (opening.has(path)) return;
  opening.add(path);
  try { await openHeadroom(path); }
  catch { await ev.action.showAlert(); }
  finally { opening.delete(path); }
}

@action({ UUID: "com.aaronfeledy.headroom.usage" })
class UsageAction extends SingletonAction<Settings> {
  private displays = new Map<string, UsageKeyDisplay>();
  override onWillAppear(ev: WillAppearEvent<Settings>): void { this.watch(ev); }
  override onDidReceiveSettings(ev: DidReceiveSettingsEvent<Settings>): void { this.watch(ev); }
  override onWillDisappear(ev: WillDisappearEvent<Settings>): void {
    poller.unsubscribe(ev.action.id);
    this.displays.get(ev.action.id)?.dispose();
    this.displays.delete(ev.action.id);
  }
  override onKeyDown(ev: KeyDownEvent<Settings>): Promise<void> { return open(ev); }
  private watch(ev: WillAppearEvent<Settings> | DidReceiveSettingsEvent<Settings>): void {
    if (!ev.action.isKey()) return;
    const key = ev.action;
    const config = settings(ev.payload.settings);
    let display = this.displays.get(key.id);
    if (display) display.configure(config);
    else {
      display = new UsageKeyDisplay(config, icons, image => key.setImage(image));
      this.displays.set(key.id, display);
    }
    const current = display;
    poller.subscribe(key.id, config.cliPath, snapshot => current.update(snapshot));
  }
}

@action({ UUID: "com.aaronfeledy.headroom.open" })
class OpenAction extends SingletonAction<Settings> {
  override onKeyDown(ev: KeyDownEvent<Settings>): Promise<void> { return open(ev); }
}

streamDeck.actions.registerAction(new UsageAction());
streamDeck.actions.registerAction(new OpenAction());
// Never log CLI output, account data, paths, or provider error text.
streamDeck.connect().catch(() => { streamDeck.logger.error("Could not connect to Stream Deck."); process.exitCode = 1; });
