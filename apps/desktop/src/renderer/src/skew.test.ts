import assert from "node:assert/strict";
import { test } from "node:test";
import { Code, ConnectError } from "@connectrpc/connect";
import { needsUpdate } from "./skew.ts";

// A machine older than this app (docs/version-skew.md §6.3).

test("unimplemented and an unknown method say the machine needs an update; nothing else does", () => {
  assert.equal(needsUpdate(new ConnectError("unknown call", Code.Unimplemented)), true);
  assert.equal(needsUpdate({ code: -32601, kind: "method_not_found", message: "unknown method" }), true);
  assert.equal(needsUpdate(new ConnectError("no", Code.PermissionDenied)), false);
  assert.equal(needsUpdate({ kind: "invalid_params" }), false);
  assert.equal(needsUpdate(new Error("x")), false);
  assert.equal(needsUpdate(null), false);
});
