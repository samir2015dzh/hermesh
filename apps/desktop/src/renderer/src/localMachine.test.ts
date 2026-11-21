import { test } from "node:test";
import assert from "node:assert/strict";
import { localMachineOff } from "./localMachine.ts";

test("localMachineOff: said while this computer is not turned on as a machine", () => {
  assert.equal(localMachineOff({ machineOnly: false, localMachineEnabled: false }), true);
});

test("localMachineOff: not said while it is on", () => {
  assert.equal(localMachineOff({ machineOnly: false, localMachineEnabled: true }), false);
});

test("localMachineOff: not said on a computer that is only a machine, which has no switch", () => {
  assert.equal(localMachineOff({ machineOnly: true, localMachineEnabled: false }), false);
});

test("localMachineOff: not said before the app knows its status", () => {
  assert.equal(localMachineOff(undefined), false);
});
