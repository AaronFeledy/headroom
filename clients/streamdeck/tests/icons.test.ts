import assert from "node:assert/strict";
import { test } from "node:test";
import { providers } from "../src/model.js";
import { loadProviderIcons } from "../src/icons.js";

test("every provider display name resolves to a bundled PNG icon", () => {
  const icons = loadProviderIcons("../shared/provider-icons");
  assert.deepEqual(Object.keys(icons).sort(), Object.values(providers).sort());
  const signature = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  for (const icon of Object.values(icons)) {
    const [prefix, payload] = icon.split(",", 2);
    assert.equal(prefix, "data:image/png;base64");
    assert.deepEqual(Buffer.from(payload, "base64").subarray(0, 8), signature);
  }
});
