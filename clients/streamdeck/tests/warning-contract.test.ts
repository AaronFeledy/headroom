import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { warningLevel } from "../src/pacing.js";
import type { Pace, WarningLevel } from "../src/pacing.js";

const fixture: {
  readonly schema: number;
  readonly cases: readonly {
    readonly name: string;
    readonly previous?: WarningLevel;
    readonly steps: readonly {
      readonly used: number;
      readonly pressure: number | null;
      readonly expected: WarningLevel;
    }[];
  }[];
} = JSON.parse(readFileSync(new URL("../../../server/internal/usage/testdata/warning_hysteresis.json", import.meta.url), "utf8"));

test("warning hysteresis fixture has a supported schema and cases", () => {
  assert.equal(fixture.schema, 1);
  assert.notEqual(fixture.cases.length, 0);
});

for (const scenario of fixture.cases) {
  test(`warning hysteresis shared contract: ${scenario.name}`, () => {
    assert.notEqual(scenario.steps.length, 0);
    let previous = scenario.previous ?? 0;
    for (const [index, step] of scenario.steps.entries()) {
      const pace: Pace = step.pressure === null
        ? { label: "", pressure: 0 }
        : { label: "", expected: 0, pressure: step.pressure };
      assert.equal(warningLevel(step.used, pace, previous), step.expected,
        `${scenario.name} step ${index + 1}: used=${step.used} pressure=${step.pressure} previous=${previous}`);
      previous = step.expected;
    }
  });
}
