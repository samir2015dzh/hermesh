import { test } from "node:test";
import assert from "node:assert/strict";
import { startPage } from "./start.ts";

test("startPage: the overview", () => {
  assert.equal(startPage({ onSession: false }), "overview");
});

test("startPage: a window that quit on a session stays on it", () => {
  assert.equal(startPage({ onSession: true }), null);
});
