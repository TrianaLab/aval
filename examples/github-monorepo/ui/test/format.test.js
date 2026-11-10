import { test } from "node:test";
import assert from "node:assert/strict";
import { duration } from "../src/format.js";

test("duration", () => {
  assert.equal(duration(42), "42s");
  assert.equal(duration(252), "4m12s");
});
