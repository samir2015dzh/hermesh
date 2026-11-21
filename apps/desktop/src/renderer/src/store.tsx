import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { ConnectError } from "@connectrpc/connect";
import type { Client, Device, GetStatusResponse, Machine, Project, Unreachable, Wake } from "@aide/proto/local";
import type { Session } from "@aide/proto/session";
import { sessionView, stateLimitsOf, unreachableMachines, withKnownState, type SessionFilter, type SessionView, type StateLimits } from "@aide/core";
import type { Peek } from "./layout";
import { startPage } from "./start";
import { useWorld } from "./useWorld";
import { i18n } from "../../shared/i18n";
import { needsUpdate } from "./skew";

// What the window knows: the owner's world as aided pushes it
// (useWorld: no polling), plus the window's own state (selection, list
// view, open sheet, toast).

export type Data = {
  /** undefined until the first answer; null when aided cannot be reached. */
  status: GetStatusResponse | null | undefined;
  clients: Client[];
  machines: Machine[];
  projects: Project[];
  projectsUnreachable: Unreachable[];
  devices: Device[];
};

export type Selection =
  | { kind: "overview" }
  | { kind: "session"; id: string }
  /** DeepSeek Harness's own sessions (dsh/DSHSessions.tsx), one of them chosen. */
  | { kind: "dsh"; machine?: string; id?: string }
  /** The Claude desktop app's Claude Code sessions (claude/ClaudeSessions.tsx), one of them chosen. */
  | { kind: "claude"; machine?: string; id?: string };

export type Sheet =
  | { kind: "newGrant" }
  | { kind: "addMachine" }
  | { kind: "addProject"; machineId?: string; agent?: string }
  /** New Session (docs/technical-design.md §3.4): a machine and agent picked first when given. */
  | { kind: "newSession"; machineId?: string; agent?: string }
  /** Contacts (docs/personas.md): write to one, which goes on in its task or starts one. */
  | { kind: "contacts" }
  | { kind: "addDevice" }
  | { kind: "scope"; clientId: string; renew: boolean }
  /**
   * An agent's doorbell (docs/agent-names.md §7): with wake, see and change it;
   * with code, set one up from the agent's wake link (name: the bot it is for).
   */
  | { kind: "wake"; clientId: string; agent: string; wake?: Wake; code?: string; name?: string }
  /**
   * With herData, removing a machine that still holds Her's data (from before Aide dropped her): the choice to
   * erase it too, given to run, or a line that it stays while the machine is offline.
   */
  | { kind: "confirm"; title: string; message: string; button: string; run: (eraseHer: boolean) => Promise<unknown>; done?: string; herData?: { name: string; offline: boolean } }
  /** Erase Everything (docs/erase-everything.md). */
  | { kind: "erase" };

export type Toast = { text: string; undo?: () => void; id: number };

/** The list by status, titles alone, until the owner picks a view (docs/her-team.md §16). */
export const defaultView = () => sessionView({ metadata: false });

type UI = {
  selection: Selection;
  inspector: boolean;
  sidebar: boolean;
  /** What shows over the conversation for now, though there is no room for it beside it (layout.ts). */
  peek: Peek;
  /** How the session list is laid out, and whether it shows archived sessions. */
  view: SessionView;
  /** The owner picked the view in its menu: otherwise the list is the default one. */
  viewChosen: boolean;
  archived: boolean;
  /** The list's collapsed groups, as "grouping:key" (each grouping keeps its own). */
  collapsed: string[];
  searching: boolean;
  /** The session whose title is being edited in the list. */
  renaming: string | null;
  /** What the search palette opens narrowed to ("Show in Main Window" in the settings). */
  searchFor: Partial<SessionFilter>;
  sheet: Sheet | null;
  toast: Toast | null;
  /** A machine only asked to become one of the owner's devices too: logging in again shows. */
  upgrading: boolean;
};

export type Store = Data &
  UI & {
    /** The window's width, which lays it out (layout.ts). */
    width: number;
    /** The sessions loaded so far: the newest page, older pages the list asked for, and every change since. */
    sessions: Session[];
    /**
     * A session as last known, even while its machine cannot be read (sessions shows its state as unknown then):
     * what states.ts works the two-part state out from (docs/session-states.md).
     */
    lastKnown: (id: string) => Session | undefined;
    /** The limits sessions' states are worked out with. */
    stateLimits: StateLimits;
    /** Moves when the counts of work changed (QueryWork). */
    workVersion: number;
    sessionsUnreachable: Unreachable[];
    /** The first page of sessions arrived. */
    sessionsLoaded: boolean;
    /** Older sessions than those loaded exist. */
    moreSessions: boolean;
    loadOlderSessions: () => void;
    set: (patch: Partial<UI>) => void;
    select: (id: string) => void;
    refresh: () => Promise<void>;
    /** Runs a change against aided, then refreshes; failures become a toast. Resolves to whether it worked. */
    run: (action: () => Promise<unknown>, done?: string) => Promise<boolean>;
    show: (text: string, undo?: () => void) => void;
  };

const Ctx = createContext<Store | null>(null);

export function useStore(): Store {
  const s = useContext(Ctx);
  if (!s) throw new Error("useStore outside AideProvider");
  return s;
}

/** Message of an error from aided, for the person. */
export function errorText(e: unknown): string {
  if (needsUpdate(e)) return i18n.t("common:needsUpdate.text");
  if (e instanceof ConnectError) return e.rawMessage;
  return e instanceof Error ? e.message : String(e);
}

function saved(key: string, fallback: boolean): boolean {
  try {
    const v = localStorage.getItem(key);
    return v === null ? fallback : v === "1";
  } catch {
    return fallback;
  }
}

export function save(key: string, v: boolean) {
  try {
    localStorage.setItem(key, v ? "1" : "0");
  } catch {
    // Preferences are a convenience; without storage they just don't stick.
  }
}

type SavedView = Pick<UI, "selection" | "sidebar" | "view" | "viewChosen" | "archived" | "collapsed">;
function savedView(): Partial<SavedView> {
  if (location.hash.startsWith("#settings")) return {};
  try {
    const v: Partial<SavedView> = JSON.parse(localStorage.getItem("main-view") ?? "{}");
    const chosen = v.viewChosen === true;
    // The list's column of earlier versions is gone: its place is the sidebar. So are Her's pages.
    const selection = ["list", "her", "progress"].includes(v.selection?.kind as string) ? undefined : v.selection;
    return { selection, sidebar: v.sidebar, view: chosen ? sessionView(v.view) : undefined, viewChosen: chosen, archived: v.archived, collapsed: Array.isArray(v.collapsed) ? v.collapsed : undefined };
  } catch {
    return {};
  }
}

export function AideProvider({ children }: { children: ReactNode }) {
  const world = useWorld();
  // Each part changes only when its own list does: a session's change leaves them all.
  const unreachable = useMemo(() => unreachableMachines(world.machines), [world.machines]);
  const status = world.down ? null : world.loaded ? world.status : undefined;
  const data = useMemo<Data>(
    () => ({ status, clients: world.clients, machines: world.machines, projects: world.projects, projectsUnreachable: unreachable, devices: world.devices }),
    [status, world.clients, world.machines, world.projects, unreachable, world.devices],
  );
  const stateLimits = useMemo(() => stateLimitsOf(world.status), [world.status]);
  const sessions = useMemo(() => withKnownState(world.sessions, world.machines, stateLimits), [world.sessions, world.machines, stateLimits]);
  const lastKnown = useMemo(() => {
    const by = new Map(world.sessions.map((s) => [s.sessionId, s]));
    return (id: string) => by.get(id);
  }, [world.sessions]);
  const [ui, setUI] = useState<UI>(() => ({
    selection: { kind: "overview" },
    inspector: saved("inspector", true),
    sidebar: true,
    peek: null,
    view: defaultView(),
    viewChosen: false,
    archived: false,
    collapsed: [],
    searching: false,
    renaming: null,
    searchFor: {},
    sheet: null,
    toast: null,
    upgrading: false,
    ...Object.fromEntries(Object.entries(savedView()).filter(([, v]) => v !== undefined)),
  }));
  // The start page, chosen once from an initialized owner's world, unless something
  // (a notification, a click) chose a page since the window opened.
  const opened = useRef(ui.selection);
  const started = useRef(false);
  useEffect(() => {
    const s = world.status;
    if (started.current || location.hash.startsWith("#settings") || !world.loaded || !s || !(s.initialized || s.machineOnly)) return;
    started.current = true;
    const kind = startPage({ onSession: opened.current.kind === "session" });
    if (kind) setUI((u) => (u.selection === opened.current ? { ...u, selection: { kind } } : u));
  }, [world.loaded, world.status]);
  useEffect(() => {
    if (location.hash.startsWith("#settings")) return;
    try { localStorage.setItem("main-view", JSON.stringify({ selection: ui.selection, sidebar: ui.sidebar, view: ui.view, viewChosen: ui.viewChosen, archived: ui.archived, collapsed: ui.collapsed })); }
    catch { /* Main-window context is optional when storage is unavailable. */ }
  }, [ui.selection, ui.sidebar, ui.view, ui.viewChosen, ui.archived, ui.collapsed]);
  const [width, setWidth] = useState(() => window.innerWidth);
  useEffect(() => {
    const resize = () => setWidth(window.innerWidth);
    window.addEventListener("resize", resize);
    return () => window.removeEventListener("resize", resize);
  }, []);
  const toastTimer = useRef<ReturnType<typeof setTimeout>>(undefined);

  // Watch brings every change; a step that waits on one (signing in) reads the status at once.
  const refresh = world.refreshStatus;

  const set = useCallback((patch: Partial<UI>) => {
    if (patch.inspector !== undefined) save("inspector", patch.inspector);
    setUI((u) => ({ ...u, ...patch }));
  }, []);

  const show = useCallback((text: string, undo?: () => void) => {
    clearTimeout(toastTimer.current);
    const id = Date.now();
    setUI((u) => ({ ...u, toast: { text, undo, id } }));
    toastTimer.current = setTimeout(() => setUI((u) => (u.toast?.id === id ? { ...u, toast: null } : u)), 5200);
  }, []);

  const run = useCallback(
    async (action: () => Promise<unknown>, done?: string) => {
      try {
        await action();
        if (done) show(done);
        await refresh();
        return true;
      } catch (e) {
        show(errorText(e));
        return false;
      }
    },
    [refresh, show],
  );

  const select = useCallback((id: string) => setUI((u) => ({ ...u, selection: { kind: "session", id } })), []);

  const store = useMemo<Store>(
    () => ({
      ...data,
      sessions,
      lastKnown,
      stateLimits,
      sessionsUnreachable: data.projectsUnreachable,
      sessionsLoaded: world.loaded && world.status !== undefined && (world.status.initialized || world.status.machineOnly),
      moreSessions: world.next !== undefined,
      loadOlderSessions: world.loadOlder,
      workVersion: world.workVersion,
      ...ui,
      width,
      set,
      select,
      refresh,
      run,
      show,
    }),
    [data, sessions, lastKnown, stateLimits, world.loaded, world.status, world.next, world.loadOlder, world.workVersion, ui, width, set, select, refresh, run, show],
  );
  return <Ctx.Provider value={store}>{children}</Ctx.Provider>;
}

/**
 * The current time, ticking every interval ms (for elapsed times and
 * countdowns). One clock per interval for the whole window, so everything
 * on the same interval changes in the same frame. Ticks fall halfway
 * between wall-clock multiples of interval, so a clock counting whole
 * seconds (elapsedSeconds) moves by exactly one on each, however late a
 * tick comes.
 */
export function useNow(interval = 1000): Date {
  const c = clock(interval);
  return useSyncExternalStore(c.subscribe, c.now);
}

const clocks = new Map<number, { subscribe: (f: () => void) => () => void; now: () => Date }>();

function clock(interval: number) {
  let c = clocks.get(interval);
  if (c) return c;
  const subs = new Set<() => void>();
  let now = new Date();
  let timer: ReturnType<typeof setTimeout> | undefined;
  c = {
    subscribe(f) {
      if (subs.size === 0) {
        now = new Date();
        const next = () => {
          timer = setTimeout(() => {
            now = new Date();
            subs.forEach((g) => g());
            next();
          }, interval - ((Date.now() + interval / 2) % interval));
        };
        next();
      }
      subs.add(f);
      return () => {
        subs.delete(f);
        if (subs.size === 0) clearTimeout(timer);
      };
    },
    now: () => now,
  };
  clocks.set(interval, c);
  return c;
}
