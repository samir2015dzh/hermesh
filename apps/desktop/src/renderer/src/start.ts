// Where the main window opens: the overview. A window that quit on a session opens on it again, with its
// unfinished answer (decided by the owner on 2026-11-15): null, the window stays where it was.

export function startPage(o: { onSession: boolean }): "overview" | null {
  return o.onSession ? null : "overview";
}
