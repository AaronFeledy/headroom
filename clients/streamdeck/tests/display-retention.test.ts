import assert from "node:assert/strict";
import { test } from "node:test";
import { UsageKeyDisplay } from "../src/display.js";
import { FakeClock, flush, svg, reading, rateLimited } from "./display-fixtures.js";

test("stale keys freeze the ring and marker, recover without pulses, and clear on configure", async () => {
  const time = new FakeClock(), images: string[] = [];
  const display = new UsageKeyDisplay({}, {}, async image => { images.push(svg(image)); }, time);
  try {
    display.update(reading(35)); await flush();
    const live = images.at(-1) ?? "";
    time.advance(3600000);
    display.update(rateLimited()); await flush();
    const stale = images.at(-1) ?? "";
    assert.match(stale, /data-role="usage"[^>]+stroke="#6272a4"/);
    assert.equal(stale.match(/stroke-dasharray="[^"]+"/)?.[0], live.match(/stroke-dasharray="[^"]+"/)?.[0]);
    assert.equal(stale.match(/<g data-role="pace-marker">.*?<\/g>/)?.[0], live.match(/<g data-role="pace-marker">.*?<\/g>/)?.[0]);
    time.advance(3600000);
    display.update(rateLimited()); await flush();
    assert.equal(images.at(-1), stale);
    assert.equal(time.jobs.size, 0);
    display.update(reading(100)); await flush();
    assert.match(images.at(-1) ?? "", /data-role="usage"[^>]+stroke="#ff5555"/);
    assert.equal(time.jobs.size, 0);
    display.update(rateLimited()); await flush();
    display.configure({});
    display.update(rateLimited()); await flush();
    assert.match(images.at(-1) ?? "", /No data/);
    assert.doesNotMatch(images.at(-1) ?? "", /data-role="usage"|private provider error/);
    assert.equal(time.jobs.size, 0);
  } finally {
    display.dispose();
  }
});
