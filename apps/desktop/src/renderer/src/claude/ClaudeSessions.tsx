import { useCallback, useEffect, useState } from "react";
import { LocalMachineOff } from "../dsh/LocalMachineOff";
import { ToolRow } from "../dsh/ToolRow";
import { MachineGroupHead, MachineTag } from "../MachineTag";
import { groupByMachine, showGroups } from "../machineScope";
import { useTranslation } from "react-i18next";
import { agoText, dateOf, lostLine, transcript } from "@aide/core";
import type { ClaudeSessionEntry, ClaudeIndexEntry, Unreachable } from "@aide/proto/local";
import { EventView, type SessionEvent } from "@aide/proto/session";
import { local } from "../api";
import { errorText, useNow, useStore } from "../store";
import "../dsh/dsh.css";

// The Claude Code sessions the Claude desktop app lists on the owner's machines, which Aide did not start
// (docs/claude-sessions.md): their list by title and last activity, the GitHub repository a session works
// in when its folder's own origin says so, and what Claude Code wrote of each. Read only. A machine that
// can't be reached keeps its sessions here as last read, lost.

const pageSize = 200;

export function ClaudeSessions() {
  const { t } = useTranslation("common");
  const st = useStore();
  const now = useNow(30_000);
  const [entries, setEntries] = useState<ClaudeSessionEntry[]>();
  const [unreachable, setUnreachable] = useState<Unreachable[]>([]);
  const [indexes, setIndexes] = useState<ClaudeIndexEntry[]>([]);
  const [error, setError] = useState("");
  const [archived, setArchived] = useState(false);
  const sel = st.selection.kind === "claude" ? st.selection : undefined;
  const load = useCallback(() => {
    local.listClaudeSessions({}).then(
      (r) => (setEntries(r.sessions), setUnreachable(r.unreachable), setIndexes(r.indexes), setError("")),
      (e) => setError(errorText(e)),
    );
  }, []);
  useEffect(() => {
    load();
    const i = setInterval(load, 15_000);
    return () => clearInterval(i);
  }, [load]);
  const shown = (entries ?? []).filter((e) => archived || !e.session?.archived);
  const nArchived = (entries ?? []).length - shown.length;
  const chosen = sel?.id ? entries?.find((e) => e.machineId === sel.machine && e.session?.sessionId === sel.id) : undefined;
  // Several machines: the list by machine, each group its header, so no two machines' sessions run together.
  const grouped = showGroups(st.machines);
  const nameOf = (id: string) => st.machines.find((m) => m.machineId === id)?.machineName || id;
  // The app's own word after the session's last turn; lost, that word as last known (never as now).
  const statusText = (status: string) =>
    status === "completed" ? t("claude.completed") : status === "blocked" ? t("claude.blocked") : status ? status : "";
  const line = (e: ClaudeSessionEntry, withTime: boolean) => {
    const s = e.session!;
    const at = dateOf(s.lastActivityAt);
    const read = dateOf(e.readAt);
    return [
      e.lost ? lostLine(statusText(s.status) || t("dsh.unknown"), read ? now.getTime() - read.getTime() : 0) : statusText(s.status),
      withTime && at ? agoText(at, now) : "",
      withTime && grouped ? "" : e.machineName,
      s.repo || baseName(s.cwd),
      s.archived ? t("dsh.archived") : "",
    ]
      .filter(Boolean)
      .join(" · ");
  };
  return (
    <>
      <div className="ctool drag">
        <div className="ctitle" />
      </div>
      <div className="dsh">
        <nav className="dsh-list" aria-label={t("claude.title")}>
          <h1>{t("claude.title")}</h1>
          <p className="dsh-note">{t("claude.note")}</p>
          <LocalMachineOff done={load} />
          {error && <p className="dsh-note warn">{error}</p>}
          {unreachable.map((u) => (
            <p key={u.machineId} className="dsh-note warn">
              {t(entries?.some((e) => e.lost && e.machineId === u.machineId) ? "dsh.unreachableLastKnown" : "dsh.unreachable", { name: u.machineName || u.machineId })}
            </p>
          ))}
          {entries && shown.length === 0 && (
            <div className="dsh-empty">
              <p>{t("claude.none")}</p>
              {indexes.map((x) => (
                <p key={x.machineId} className="dsh-home">
                  {x.indexes.length > 0 ? (
                    <>
                      {t("dsh.readFrom", { name: nameOf(x.machineId) })} <span className="mono sel-text">{x.indexes.join(", ")}</span>
                    </>
                  ) : (
                    t("claude.noApp", { name: nameOf(x.machineId) })
                  )}
                </p>
              ))}
            </div>
          )}
          {groupByMachine(shown, (e) => e.machineId, st.machines).map((g) => (
            <div key={g.machineId} role="group" aria-label={g.name}>
              {grouped && <MachineGroupHead name={g.name} local={g.local} count={g.items.length} />}
              {g.items.map((e) => {
            const s = e.session!;
            return (
              <button
                key={`${e.machineId}/${s.sessionId}`}
                className={`dsh-row ${chosen === e ? "sel" : ""}`}
                aria-current={chosen === e || undefined}
                onClick={() => st.set({ selection: { kind: "claude", machine: e.machineId, id: s.sessionId } })}
              >
                <i className={`dsh-dot ${e.lost ? "lost" : "ended"}`} aria-hidden="true" />
                <span className="t">
                  <b>{s.title || s.sessionId}</b>
                  <span>{line(e, true)}</span>
                </span>
              </button>
            );
              })}
            </div>
          ))}
          {nArchived > 0 || archived ? (
            <button className="link dsh-toggle" onClick={() => setArchived(!archived)}>
              {archived ? t("dsh.hideArchived") : t("dsh.showArchived", { count: nArchived })}
            </button>
          ) : null}
        </nav>
        <div className="dsh-view">
          {chosen ? (
            <ClaudeTranscript key={`${chosen.machineId}/${chosen.session!.sessionId}`} entry={chosen} line={line(chosen, false)} />
          ) : (
            <p className="dsh-empty">{t("dsh.pick")}</p>
          )}
        </div>
      </div>
    </>
  );
}

function baseName(p: string): string {
  const parts = p.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] ?? "";
}

/** What Claude Code wrote of one session, newest page first; earlier pages on request. Lost: what was last read only. */
function ClaudeTranscript({ entry, line }: { entry: ClaudeSessionEntry; line: string }) {
  const { t } = useTranslation("common");
  const s = entry.session!;
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [more, setMore] = useState(false);
  const [error, setError] = useState(s.error);
  const [read, setRead] = useState(false);
  const page = useCallback(
    (beforeSeq: bigint) =>
      local.readClaudeSession({ machineId: entry.machineId, sessionId: s.sessionId, beforeSeq, limit: pageSize, view: EventView.FULL }).then(
        (r) => {
          setEvents((old) => (beforeSeq > 0n ? [...r.events, ...old] : r.events));
          setMore(r.hasMoreBefore);
          setError(r.session?.error ?? "");
          setRead(true);
        },
        (e) => (setError(errorText(e)), setRead(true)),
      ),
    [entry.machineId, s.sessionId],
  );
  useEffect(() => {
    if (!entry.lost) page(0n);
  }, [page, entry.lost, s.lastActivityAt?.seconds]);
  const items = transcript(events, false);
  return (
    <>
      <div className="dsh-head">
        <div className="dsh-head-row">
          <h2>{s.title || s.sessionId}</h2>
          <MachineTag machineId={entry.machineId} />
        </div>
        <span>{[line, s.model, s.cwd].filter(Boolean).join(" · ")}</span>
      </div>
      {entry.lost ? (
        <p className="dsh-empty">{t("dsh.lostNote", { name: entry.machineName || entry.machineId })}</p>
      ) : (
        <>
          {error && <p className="dsh-msg end err">{error}</p>}
          {more && (
            <button className="btn sm" onClick={() => events.length && page(events[0].seq)}>
              {t("dsh.earlier")}
            </button>
          )}
          {items.map((it) => {
            switch (it.kind) {
              case "user":
                return (
                  <div key={String(it.seq)} className="dsh-msg user">
                    {it.text}
                  </div>
                );
              case "agent":
                return (
                  <div key={String(it.seq)} className="dsh-msg">
                    {it.text}
                  </div>
                );
              case "tool":
                return <ToolRow key={String(it.seq)} call={it.call} result={it.result} />;
              default:
                return null;
            }
          })}
          {read && items.length === 0 && !error && <p className="dsh-empty">{t("dsh.empty")}</p>}
        </>
      )}
    </>
  );
}
