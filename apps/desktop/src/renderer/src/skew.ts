import { Code, ConnectError } from "@connectrpc/connect";

// A machine whose Aide is older than this app (docs/version-skew.md).

/**
 * The error says the machine's Aide is older than this app (§6.3): aided or its services don't
 * know the call.
 */
export function needsUpdate(e: unknown): boolean {
  if (e instanceof ConnectError) return e.code === Code.Unimplemented;
  return (e as { kind?: unknown } | null)?.kind === "method_not_found";
}

