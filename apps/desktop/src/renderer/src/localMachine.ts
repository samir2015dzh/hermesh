// This computer as a machine (LocalService.SetLocalMachine): the switch the pages that list what is on the
// owner's machines depend on. While it is off this computer is none of them, so its own DSH and Claude
// sessions are not listed, and the page says why instead of looking empty.

type Status = { machineOnly: boolean; localMachineEnabled: boolean };

/**
 * Whether to say so: the app knows its status, this computer could be a machine of the owner's (one that
 * is only a machine always is one, and has no switch), and it is not turned on. Unknown status says
 * nothing: a notice that flashes while the app starts would be wrong most of the time.
 */
export function localMachineOff(status: Status | null | undefined): boolean {
  return !!status && !status.machineOnly && !status.localMachineEnabled;
}
