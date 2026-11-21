import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { parseAppLink } from "@aide/core";
import { agentName } from "@aide/ui";
import { local } from "./api";
import { Overview } from "./Overview";
import { DSHSessions } from "./dsh/DSHSessions";
import { ClaudeSessions } from "./claude/ClaudeSessions";
import { SearchPalette } from "./Search";
import { SessionPage } from "./SessionPage";
import { SheetHost } from "./Sheets";
import { Rail } from "./Rail";
import { Sidebar } from "./Sidebar";
import { useCommands } from "./commands";
import { useLeaving } from "./components";
import { Icon } from "./icons";
import { layoutOf, setSidebar, usePeekEnd } from "./layout";
import { platform } from "./platform";
import { errorText, useStore } from "./store";

export function MainWindow() {
  const st = useStore();
  useCommands();
  // The window's material shows through the sidebar, macOS's or Windows 11's (the first-run pages stay opaque).
  useEffect(() => {
    if (!window.aide?.material) return;
    document.documentElement.classList.add("vibrant");
    return () => document.documentElement.classList.remove("vibrant");
  }, []);
  const session = st.selection.kind === "session" ? st.sessions.find((s) => s.sessionId === (st.selection as { id: string }).id) : undefined;
  // A selected session that is gone (e.g. its machine forgot it, or it was deleted): the overview.
  useEffect(() => {
    if (st.selection.kind !== "session" || !st.sessionsLoaded || session) return;
    if (st.moreSessions) st.loadOlderSessions();
    else st.set({ selection: { kind: "overview" } });
  }, [st, session]);
  // "Show in Main Window" in the settings window: the search palette, narrowed to a grant or a machine.
  const { set } = st;
  useEffect(
    () =>
      window.aide?.onCommand((c) => {
        if (c.startsWith("searchGrant:")) set({ searching: true, searchFor: { grant: c.slice("searchGrant:".length) } });
        if (c.startsWith("searchMachine:")) set({ searching: true, searchFor: { machine: c.slice("searchMachine:".length) } });
      }),
    [set],
  );
  // An agent's wake link (aide://wake?code=…, docs/agent-names.md §7.2): its setup, at the routine's URL and key.
  const clients = useRef(st.clients);
  clients.current = st.clients;
  const { show } = st;
  useEffect(
    () =>
      window.aide?.onLink((url) => {
        const l = parseAppLink(url);
        if (l?.kind !== "wake") return;
        local.openWakeLink({ code: l.code }).then((o) => {
          const c = clients.current.find((x) => x.clientId === o.clientId);
          set({ sheet: { kind: "wake", clientId: o.clientId, agent: c ? agentName(c.label) : o.clientId, code: l.code, name: o.name } });
        }, (e) => show(errorText(e)));
      }),
    [set, show],
  );
  // A peek ends when it fits again, when another session or page is chosen, and with Esc.
  usePeekEnd(st);
  useEffect(() => {
    set({ peek: null });
  }, [st.selection, set]);
  const peeking = st.peek !== null;
  useEffect(() => {
    if (!peeking) return;
    const esc = (e: KeyboardEvent) => e.key === "Escape" && !e.defaultPrevented && set({ peek: null });
    document.addEventListener("keydown", esc);
    return () => document.removeEventListener("keydown", esc);
  }, [peeking, set]);
  const { sidebar } = layoutOf(st);
  // The search, keyed by what it was opened for; closed, it fades out as it was.
  const search = useLeaving(st.searching ? JSON.stringify(st.searchFor) : null);
  return (
    <div className={`mainwrap ${sidebar === "docked" ? "" : "railed"} ${sidebar === "over" ? "peek" : ""}`}>
      {/* The sidebar, or its rail (docs/her-team.md §14.9): asked for while there is no room, it shows over the rail. */}
      {sidebar === "docked" ? <Sidebar /> : <Rail />}
      {sidebar === "over" && <Sidebar />}
      {/* Over the conversation, the sidebar goes again with a click beside it. */}
      {sidebar === "over" && <div className="peek-scrim" onClick={() => setSidebar(st, false)} />}
      <section className="content">
        {session ? (
          <SessionPage s={session} />
        ) : st.selection.kind === "dsh" && !st.status?.machineOnly ? (
          <DSHSessions />
        ) : st.selection.kind === "claude" && !st.status?.machineOnly ? (
          <ClaudeSessions />
        ) : (
          <Overview />
        )}
      </section>
      {search.shown !== null && <SearchPalette key={search.shown} leaving={search.leaving} />}
      <SheetHost />
      <ToastView />
    </div>
  );
}

/** Windows: the app menu, under its button (there is no menu bar; main/menu.ts). */
export function AppMenuButton() {
  const { t } = useTranslation("common");
  if (platform !== "win") return null;
  return (
    <button
      className="tb"
      aria-label={t("window.menu")}
      title={t("window.menu")}
      onClick={(e) => {
        const r = e.currentTarget.getBoundingClientRect();
        window.aide?.showAppMenu(r.left, r.bottom + 4);
      }}
    >
      <Icon name="menu" />
    </button>
  );
}

export function ToastView() {
  const st = useStore();
  const { t } = useTranslation("common");
  const { shown: toast, leaving } = useLeaving(st.toast);
  if (!toast) return null;
  return (
    <div className={`toast ${leaving ? "leaving" : ""}`} inert={leaving} role="status">
      <Icon name="check" />
      <span>{toast.text}</span>
      {toast.undo && (
        <button className="btn sm" onClick={() => (st.set({ toast: null }), toast.undo?.())}>{t("window.undo")}</button>
      )}
    </div>
  );
}
