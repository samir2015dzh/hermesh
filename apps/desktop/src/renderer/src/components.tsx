import { SessionLink, SessionStatus, type Session } from "@aide/proto/session";
import { codingAgentShort, codingAgentTitle, isActive, isFailed, livenessOf, sessionStateOf, stateLine, statusTitle, type StateView } from "@aide/core";
import { useNow, useStore } from "./store";
import { AgentLogo, AgentTile, grantColor, grantLogo, grantPicture, type GrantLook, type PersonalAgentKind } from "@aide/ui";
import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Icon } from "./icons";

/** How long a sheet, the search or a toast takes to go (`.leaving` in app.css), drawn as it was. */
export const LEAVE_MS = 140;

/**
 * `value` while it is there, and its last value for LEAVE_MS more once it goes (`leaving`),
 * for what shows it to fade out instead of vanishing. A new value shows at once.
 */
export function useLeaving<T>(value: T | null | undefined): { shown: T | null; leaving: boolean } {
  const [last, setLast] = useState<T | null>(value ?? null);
  if (value != null && value !== last) setLast(value);
  const leaving = value == null && last !== null;
  useEffect(() => {
    if (!leaving) return;
    const id = setTimeout(() => setLast(null), LEAVE_MS);
    return () => clearTimeout(id);
  }, [leaving]);
  return { shown: value ?? last, leaving };
}

/**
 * What a session is doing and how fresh that is (docs/session-states.md), from what is last known of it and its
 * machine, at a clock that ticks each second.
 */
export function useStateView(s: Session, interval = 1000): StateView {
  const st = useStore();
  const now = useNow(interval);
  const raw = st.lastKnown(s.sessionId) ?? s;
  return sessionStateOf(raw, livenessOf(st.machines.find((m) => m.machineId === s.machineId)), st.stateLimits, now);
}

/** A session's state in a line, both parts: "Running · quiet 12 min", "Lost · last known Running · 7 min ago". */
export function StateText({ s }: { s: Session }) {
  return <>{stateLine(useStateView(s))}</>;
}

/** Whether the line says more than the status alone: stalled, or its machine not live. */
export function linkShown(v: StateView): boolean {
  return v.link !== SessionLink.LIVE && v.link !== SessionLink.UNSPECIFIED;
}

/** The session's state as a small dot: orange waits on you, red failed, green runs. */
export function StatusDot({ s }: { s: Session }) {
  const { t } = useTranslation("common");
  const c =
    s.status === SessionStatus.WAITING_INPUT ? "w"
    : isFailed(s) ? "c"
    : s.status === SessionStatus.RUNNING ? "r"
    : isActive(s) ? "p"
    : s.status === SessionStatus.ARCHIVED ? "a"
    : "i";
  return <span className={`sdot ${c}`} role="img" aria-label={isFailed(s) && s.status !== SessionStatus.CRASHED ? t("status.failed") : statusTitle(s.status)} />;
}

/** A grant's picture or app logo, when it has one (null: draw its initial). */
export function grantFace(c: GrantLook, size: number): ReactNode {
  const pic = grantPicture(c);
  if (pic) return <img className="gpic" src={pic} alt="" style={{ width: size, height: size, borderRadius: size * 0.28 }} />;
  const logo = grantLogo(c);
  return logo ? <AgentTile agent={logo} size={size} /> : null;
}

export function GrantAvatar({ c, size = "" }: { c: GrantLook; size?: "" | "md" | "lg" | "xl" | "sm" }) {
  const style = size === "sm" ? { width: 16, height: 16, fontSize: 9 } : undefined;
  const face = grantFace(c, { sm: 16, md: 28, lg: 40, xl: 52, "": 22 }[size]);
  if (face) return face;
  return (
    <span className={`av ${size === "sm" ? "" : size}`} style={{ background: grantColor(c.label), ...style }} aria-hidden="true">
      {c.label.slice(0, 1).toUpperCase()}
    </span>
  );
}

/** A personal agent kind's icon: its app's, or for Other Agent a plain tile. */
export function PersonalAgentIcon({ kind, size }: { kind: PersonalAgentKind; size: number }) {
  if (kind.logo) return <AgentTile agent={kind.logo} size={size} />;
  return (
    <span className="pa-icon other" style={{ width: size, height: size }} aria-hidden="true">
      <i>
        <Icon name="terminal" />
      </i>
    </span>
  );
}

/** A coding agent: its logo and (short or full) name. */
export function AgentName({ agent, full }: { agent: string; full?: boolean }) {
  return (
    <span className="agc">
      <AgentLogo agent={agent} size={15} />
      {full ? codingAgentTitle(agent) : codingAgentShort(agent)}
    </span>
  );
}

export type MenuEntry = { id: string; label: string; run: () => void };

/**
 * A menu drawn over the page (opaque), under the element it opened from and
 * aligned to its left edge (above it when there is no room below). Arrows
 * move, Esc or a press outside closes; opened by the keyboard, its first
 * item has the focus. It is drawn on the page itself: inside a card with a
 * backdrop filter, fixed would place it by the card.
 */
export function PopMenu({ label, items, anchor, keyboard, onClose }: { label: string; items: MenuEntry[]; anchor: HTMLElement; keyboard: boolean; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: -9999, top: 0 });
  useLayoutEffect(() => {
    const el = ref.current!;
    const r = anchor.getBoundingClientRect();
    const top = r.bottom + 4 + el.offsetHeight > innerHeight - 8 ? r.top - 4 - el.offsetHeight : r.bottom + 4;
    setPos({ left: Math.max(8, Math.min(r.left, innerWidth - 8 - el.offsetWidth)), top });
    (keyboard ? el.querySelector<HTMLElement>("[role=menuitem]") : el)?.focus();
  }, [anchor, keyboard]);
  useEffect(() => {
    // Its own anchor toggles it (on click), so a press there isn't outside.
    const outside = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && !anchor.contains(e.target as Node) && onClose();
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", onClose);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", onClose);
    };
  }, [anchor, onClose]);
  const onKeyDown = (e: ReactKeyboardEvent) => {
    e.stopPropagation();
    const all = [...(ref.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [])];
    const i = all.indexOf(document.activeElement as HTMLElement);
    const go = { ArrowDown: (i + 1) % all.length, ArrowUp: i <= 0 ? all.length - 1 : i - 1 }[e.key];
    if (go !== undefined) {
      e.preventDefault();
      all[go]?.focus();
    } else if (e.key === "Escape" || e.key === "Tab") {
      e.preventDefault();
      onClose();
      anchor.focus();
    }
  };
  return createPortal(
    <div className="vmenu" role="menu" aria-label={label} tabIndex={-1} ref={ref} style={pos} onKeyDown={onKeyDown}>
      {items.map((it) => (
        <button key={it.id} className="mi" role="menuitem" tabIndex={-1} onMouseMove={(e) => e.currentTarget.focus()} onClick={() => (onClose(), it.run())}>
          {it.label}
        </button>
      ))}
    </div>,
    document.body,
  );
}

/**
 * A link to the Connect guide (Settings › Connect), at one line ("deepseek",
 * "claude", "codex", "custom", "assistants") when given: from the places a
 * person looks for how to connect an agent (docs/accounts.md §3).
 */
export function ConnectLink({ line, children }: { line?: string; children: ReactNode }) {
  return (
    <button type="button" className="link" onClick={() => window.aide?.openSettings(line ? `connect/${line}` : "connect")}>
      {children}
    </button>
  );
}
