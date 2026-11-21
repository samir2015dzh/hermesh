import { useCallback, useEffect, useRef, useState } from "react";
import { Icon } from "../icons";
import { LocalMachineOff } from "./LocalMachineOff";
import { ToolRow } from "./ToolRow";
import { MachineGroupHead, MachineTag, WritesTo } from "../MachineTag";
import { groupByMachine, showGroups } from "../machineScope";
import { useTranslation } from "react-i18next";
import { agoText, dateOf, lostLine, transcript } from "@aide/core";
import { DSHSessionState, type DSHBridge, type DSHSession, type DSHSessionOptionsResponse, type PrepareDSHBridgeResponse } from "@aide/proto/machine";
import type { DSHSessionEntry, Unreachable } from "@aide/proto/local";
import { EventView, type SessionEvent } from "@aide/proto/session";
import { local } from "../api";
import { bridgeView, newRequestId, retryKeepsId } from "../dshTakeover";
import { Switch } from "../settings/common";
import { errorText, useNow, useStore } from "../store";
import "./dsh.css";

// DeepSeek Harness's own sessions on the owner's machines: those opened in DSH Desktop, or by another
// tool, which Aide did not start: their list (by last activity) and what DSH wrote of each. Read
// only, until the owner takes one over (docs/dsh-takeover.md): then Aide sends it their messages
// through the bridge plugin in DSH Desktop, queued after a turn that runs, as DSH queues its own.
// A machine that can't be reached keeps its sessions here as last read, lost: their state is last
// known, never shown as what they do now (docs/session-states.md), and nothing can be sent to them.

const stateClass: Record<DSHSessionState, string> = {
  [DSHSessionState.DSH_SESSION_STATE_UNSPECIFIED]: "unknown",
  [DSHSessionState.DSH_SESSION_STATE_RUNNING]: "running",
  [DSHSessionState.DSH_SESSION_STATE_IDLE]: "idle",
  [DSHSessionState.DSH_SESSION_STATE_ENDED]: "ended",
};

const pageSize = 200;

export function DSHSessions() {
  const { t } = useTranslation("common");
  const st = useStore();
  const now = useNow(30_000);
  const [entries, setEntries] = useState<DSHSessionEntry[]>();
  const [unreachable, setUnreachable] = useState<Unreachable[]>([]);
  const [bridges, setBridges] = useState<Record<string, DSHBridge | undefined>>({});
  const [homes, setHomes] = useState<{ machineId: string; home: string }[]>([]);
  const [error, setError] = useState("");
  const [archived, setArchived] = useState(false);
  const sel = st.selection.kind === "dsh" ? st.selection : undefined;
  const load = useCallback(() => {
    local.listDSHSessions({}).then(
      (r) => (
        setEntries(r.sessions),
        setUnreachable(r.unreachable),
        setBridges(Object.fromEntries(r.bridges.map((b) => [b.machineId, b.bridge]))),
        setHomes(r.bridges.filter((b) => b.dshHome).map((b) => ({ machineId: b.machineId, home: b.dshHome }))),
        setError("")
      ),
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
  const stateText = (e: DSHSessionEntry) => {
    const s = e.session!;
    const said = ownStateText(s);
    const read = dateOf(e.readAt);
    return e.lost ? lostLine(said, read ? now.getTime() - read.getTime() : 0) : said;
  };
  const ownStateText = (s: DSHSession) =>
    s.state === DSHSessionState.DSH_SESSION_STATE_RUNNING
      ? t("dsh.running")
      : s.state === DSHSessionState.DSH_SESSION_STATE_IDLE
        ? t("dsh.idle")
        : s.state === DSHSessionState.DSH_SESSION_STATE_ENDED
          ? t(s.turnOpen ? "dsh.endedMidTurn" : "dsh.ended")
          : t("dsh.unknown");
  // Several machines: the list by machine, each group its header, so no two machines' sessions run together.
  const grouped = showGroups(st.machines);
  return (
    <>
      <div className="ctool drag">
        <div className="ctitle" />
      </div>
      <div className="dsh">
        <nav className="dsh-list" aria-label={t("dsh.title")}>
          <h1>{t("dsh.title")}</h1>
          <p className="dsh-note">{t("dsh.note")}</p>
          <LocalMachineOff done={load} />
          {error && <p className="dsh-note warn">{error}</p>}
          {unreachable.map((u) => (
            <p key={u.machineId} className="dsh-note warn">
              {t(entries?.some((e) => e.lost && e.machineId === u.machineId) ? "dsh.unreachableLastKnown" : "dsh.unreachable", { name: u.machineName || u.machineId })}
            </p>
          ))}
          {entries && shown.length === 0 && (
            <div className="dsh-empty">
              <p>{t("dsh.none")}</p>
              {homes.map((h) => (
                <p key={h.machineId} className="dsh-home">
                  {t("dsh.readFrom", { name: st.machines.find((m) => m.machineId === h.machineId)?.machineName || h.machineId })} <span className="mono sel-text">{h.home}</span>
                </p>
              ))}
              {homes.length > 0 && <p className="dsh-home">{t("dsh.homeHint")}</p>}
            </div>
          )}
          {groupByMachine(shown, (e) => e.machineId, st.machines).map((g) => (
            <div key={g.machineId} role="group" aria-label={g.name}>
              {grouped && <MachineGroupHead name={g.name} local={g.local} count={g.items.length} />}
              {g.items.map((e) => {
            const s = e.session!;
            const at = dateOf(s.lastActivityAt);
            return (
              <button
                key={`${e.machineId}/${s.sessionId}`}
                className={`dsh-row ${chosen === e ? "sel" : ""}`}
                aria-current={chosen === e || undefined}
                onClick={() => st.set({ selection: { kind: "dsh", machine: e.machineId, id: s.sessionId } })}
              >
                <i className={`dsh-dot ${e.lost ? "lost" : stateClass[s.state]}`} title={stateText(e)} aria-hidden="true" />
                <span className="t">
                  <b>{s.title || s.sessionId}</b>
                  <span>
                    {[stateText(e), at ? agoText(at, now) : "", grouped ? "" : e.machineName, baseName(s.cwd), s.archived ? t("dsh.archived") : ""].filter(Boolean).join(" · ")}
                  </span>
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
            chosen.lost ? (
              <LostSession entry={chosen} stateText={stateText(chosen)} />
            ) : (
              <DSHTranscript key={`${chosen.machineId}/${chosen.session!.sessionId}`} entry={chosen} stateText={stateText(chosen)} bridge={bridges[chosen.machineId]} changed={load} />
            )
          ) : (
            <p className="dsh-empty">{t("dsh.pick")}</p>
          )}
        </div>
      </div>
    </>
  );
}

/** A session whose machine can't be reached: what was last read of it, and that its content waits for the machine. */
function LostSession({ entry, stateText }: { entry: DSHSessionEntry; stateText: string }) {
  const { t } = useTranslation("common");
  const s = entry.session!;
  return (
    <>
      <div className="dsh-head">
        <div className="dsh-head-row">
          <h2>{s.title || s.sessionId}</h2>
          <MachineTag machineId={entry.machineId} />
        </div>
        <span>{[stateText, entry.machineName, s.cwd].filter(Boolean).join(" · ")}</span>
      </div>
      <p className="dsh-empty">{t("dsh.lostNote", { name: entry.machineName || entry.machineId })}</p>
    </>
  );
}

function baseName(p: string): string {
  const parts = p.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] ?? "";
}

/**
 * What DSH wrote of one session, newest page first; earlier pages on request. Taken over, a box to send
 * it messages and a way to stop its turn. changed: the list should be read again.
 */
function DSHTranscript({ entry, stateText, bridge, changed }: { entry: DSHSessionEntry; stateText: string; bridge: DSHBridge | undefined; changed: () => void }) {
  const { t } = useTranslation("common");
  const s = entry.session!;
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [more, setMore] = useState(false);
  const [error, setError] = useState(s.error);
  const read = useCallback(
    (beforeSeq: bigint) =>
      local.readDSHSession({ machineId: entry.machineId, sessionId: s.sessionId, beforeSeq, limit: pageSize, view: EventView.FULL }).then(
        (r) => {
          setEvents((old) => (beforeSeq > 0n ? [...r.events, ...old] : r.events));
          setMore(r.hasMoreBefore);
          setError(r.session?.error ?? "");
        },
        (e) => setError(errorText(e)),
      ),
    [entry.machineId, s.sessionId],
  );
  useEffect(() => {
    read(0n);
  }, [read, s.lastActivityAt?.seconds]);
  const items = transcript(events, s.state === DSHSessionState.DSH_SESSION_STATE_RUNNING);
  const [takeover, setTakeover] = useState(s.takeover);
  const [asking, setAsking] = useState(false);
  const [switchError, setSwitchError] = useState("");
  useEffect(() => setTakeover(s.takeover), [s.takeover]);
  const setOn = (on: boolean) => {
    setSwitchError("");
    local.setDSHTakeover({ machineId: entry.machineId, sessionId: s.sessionId, on }).then(
      (r) => (setTakeover(r.session?.takeover ?? on), setAsking(false), changed()),
      (e) => setSwitchError(errorText(e)),
    );
  };
  return (
    <>
      <div className="dsh-head">
        <div className="dsh-head-row">
          <h2>{s.title || s.sessionId}</h2>
          <MachineTag machineId={entry.machineId} />
        </div>
        <span>{[stateText, entry.machineName, s.cwd].filter(Boolean).join(" · ")}</span>
      </div>
      {error && <p className="dsh-msg end err">{error}</p>}
      {more && (
        <button className="btn sm" onClick={() => events.length && read(events[0].seq)}>
          {t("dsh.earlier")}
        </button>
      )}
      {items.map((it) => {
        switch (it.kind) {
          case "user":
            // Typed in DSH, or sent from Aide (taken over): by the owner, or by one of their agents, named.
            return (
              <div key={String(it.seq)} className="dsh-msg user">
                {it.from === "owner" && <span className="dsh-via">{t("dsh.viaAide")}</span>}
                {it.from !== "" && it.from !== "owner" && <span className="dsh-via">{t("dsh.viaAideBy", { name: it.fromName || it.from })}</span>}
                {it.text}
              </div>
            );
          case "agent":
            return <div key={String(it.seq)} className="dsh-msg">{it.text}</div>;
          case "tool":
            return <ToolRow key={String(it.seq)} call={it.call} result={it.result} />;
          case "turnEnd":
            return it.end.isError ? (
              <div key={String(it.seq)} className="dsh-msg end err">
                {it.end.failure?.message || t("dsh.turnFailed")}
              </div>
            ) : null;
          default:
            return null;
        }
      })}
      {items.length === 0 && !error && <p className="dsh-empty">{t("dsh.empty")}</p>}
      {/* Always there (the owner's ruling, 2026-11-21): read only until taken over, the switch in it, beside sending. */}
      <Composer
        entry={entry}
        bridge={bridge}
        takeover={takeover}
        asking={asking}
        switchError={switchError}
        ask={(on) => (on ? setAsking(true) : (setAsking(false), setOn(false)))}
        confirm={() => setOn(true)}
        cancel={() => setAsking(false)}
        sent={() => (changed(), setTimeout(() => read(0n), 1500))}
        failed={changed}
      />
    </>
  );
}

/**
 * Installing the bridge plugin: DSH Desktop's own plugin manager does it (Plugins page), checking it
 * against the DSH there, restoring the profile when it fails, and turning it off or removing it later.
 * Aide prepares: it backs up DSH's configuration and writes the plugin's package, whose path it shows.
 */
function Install({ machineId }: { machineId: string }) {
  const { t } = useTranslation("common");
  const [prepared, setPrepared] = useState<PrepareDSHBridgeResponse>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  const prepare = () => {
    setBusy(true);
    setError("");
    local.prepareDSHBridge({ machineId }).then(
      (r) => (setPrepared(r), setBusy(false)),
      (e) => (setError(errorText(e)), setBusy(false)),
    );
  };
  if (!prepared) {
    return (
      <div className="dsh-install">
        <button className="btn sm" onClick={prepare} disabled={busy}>
          {busy ? t("dsh.install.preparing") : t("dsh.install.prepare")}
        </button>
        {error && <span className="err" role="alert">{error}</span>}
      </div>
    );
  }
  return (
    <div className="dsh-install" role="region" aria-label={t("dsh.install.title")}>
      <b>{t("dsh.install.title")}</b>
      <ol>
        <li>{t("dsh.install.backedUp", { count: prepared.backedUp.length, dir: prepared.backupDir })}</li>
        {!prepared.desktopProfile && <li className="warn">{t("dsh.install.noProfile")}</li>}
        <li>
          {t("dsh.install.step")}
          <span className="dsh-path">
            <span className="mono sel-text">{prepared.packagePath}</span>
            <button className="btn sm" onClick={() => navigator.clipboard.writeText(prepared.packagePath).then(() => setCopied(true))}>
              {copied ? t("dsh.install.copied") : t("dsh.install.copy")}
            </button>
          </span>
        </li>
        <li>{t("dsh.install.restart")}</li>
      </ol>
    </div>
  );
}

/**
 * Taken over: the owner's message to the session, sent through the bridge plugin in DSH Desktop. It
 * waits for the next turn when one runs (as in DSH). A failure says what to do and keeps the text; a
 * timeout keeps the message's id too, so sending again can't make two.
 */
function Composer({
  entry,
  bridge,
  takeover,
  asking,
  switchError,
  ask,
  confirm,
  cancel,
  sent,
  failed,
}: {
  entry: DSHSessionEntry;
  bridge: DSHBridge | undefined;
  takeover: boolean;
  asking: boolean;
  switchError: string;
  ask: (on: boolean) => void;
  confirm: () => void;
  cancel: () => void;
  sent: () => void;
  failed: () => void;
}) {
  const { t } = useTranslation("common");
  const s = entry.session!;
  const running = s.state === DSHSessionState.DSH_SESSION_STATE_RUNNING;
  const b = bridgeView(bridge);
  const [text, setText] = useState("");
  const reqId = useRef("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  // What the session may go on with (DSH's models and permission presets), and the owner's choice: "" keeps it as it is.
  const [options, setOptions] = useState<DSHSessionOptionsResponse>();
  const [model, setModel] = useState("");
  const [permission, setPermission] = useState("");
  useEffect(() => {
    if (!b.ok || !takeover) return;
    local.dSHSessionOptions({ machineId: entry.machineId, sessionId: s.sessionId }).then(setOptions, () => setOptions(undefined));
  }, [b.ok, takeover, entry.machineId, s.sessionId]);
  const choices = (options?.models ?? []).flatMap((m) =>
    [{ value: `${m.provider}/${m.model}/`, label: m.modelName || m.model }].concat(
      m.efforts.map((e) => ({ value: `${m.provider}/${m.model}/${e}`, label: `${m.modelName || m.model} · ${e}` })),
    ),
  );
  const current = options?.currentModel ? `${options.currentModel.provider}/${options.currentModel.model}/${options.currentModel.effort}` : "";
  const send = () => {
    if (!takeover || !text.trim() || busy) return;
    reqId.current ||= newRequestId();
    setBusy(true);
    setError("");
    setNotice("");
    const [provider, name, effort] = model.split("/");
    local.sendDSHMessage({
      machineId: entry.machineId,
      sessionId: s.sessionId,
      text,
      requestId: reqId.current,
      model: model && model !== current ? { provider, model: name, effort } : undefined,
      permission: permission && permission !== options?.currentPermission ? permission : "",
    }).then(
      () => {
        reqId.current = "";
        setText("");
        setNotice(t(running ? "dsh.queued" : "dsh.sent"));
        setBusy(false);
        sent();
      },
      (e) => {
        if (!retryKeepsId(e)) reqId.current = "";
        setError(t("dsh.notSent", { reason: errorText(e) }));
        setBusy(false);
        failed(); // the plugin's state, as it is now, above the box
      },
    );
  };
  const interrupt = () => {
    setError("");
    setNotice("");
    local.interruptDSHSession({ machineId: entry.machineId, sessionId: s.sessionId }).then(
      (r) => (setNotice(t(r.interrupted ? "dsh.interrupted" : "dsh.notRunningHere")), sent()),
      (e) => setError(errorText(e)),
    );
  };
  const off = !takeover;
  return (
    <div className={`dsh-compose ${off ? "off" : ""}`}>
      {takeover && !b.ok && (
        <p className="dsh-bridge warn" role="status">
          {t(`dsh.bridge.${b.key}`)}
          {b.key === "incompatible" && b.detail && <span>{b.detail}</span>}
        </p>
      )}
      {takeover && (b.key === "notInstalled" || b.key === "incompatible") && <Install machineId={entry.machineId} />}
      {asking && off && (
        <div className="dsh-confirm" role="dialog" aria-label={t("dsh.takeoverAsk")}>
          <b>{t("dsh.takeoverAsk")}</b>
          <WritesTo machineId={entry.machineId} />
          <ul>
            <li>{t("dsh.takeoverNote1")}</li>
            <li>{t("dsh.takeoverNote2")}</li>
            <li>{t("dsh.takeoverNote3")}</li>
          </ul>
          <div className="dsh-confirm-acts">
            <button className="btn sm" onClick={cancel}>
              {t("dsh.cancel")}
            </button>
            <button className="btn sm primary" onClick={confirm}>
              {t("dsh.takeoverOn")}
            </button>
          </div>
        </div>
      )}
      <WritesTo machineId={entry.machineId} />
      <div className="dsh-box">
        <textarea
          rows={2}
          aria-label={t("dsh.message")}
          placeholder={off ? t("dsh.takeoverToSend") : t(running ? "dsh.placeholderRunning" : "dsh.placeholder")}
          value={text}
          disabled={off || busy}
          onChange={(e) => ((reqId.current = ""), setText(e.target.value))}
          onKeyDown={(e) => {
            // Enter sends, as chat apps do; Shift+Enter is a new line; Ctrl/⌘+Enter still sends. Never while an IME composes.
            if (e.key !== "Enter" || e.shiftKey || e.altKey || e.nativeEvent.isComposing) return;
            e.preventDefault();
            send();
          }}
        />
        <div className="dsh-bar">
          <label className="dsh-takeover" title={t("dsh.takeoverAsk")}>
            <Switch on={takeover} label={t("dsh.takeover")} set={ask} />
            {t("dsh.takeover")}
          </label>
          {takeover && options && options.models.length > 0 && (
            <select className="dsh-pick" aria-label={t("dsh.model")} value={model || current} onChange={(e) => ((reqId.current = ""), setModel(e.target.value))}>
              {!current && <option value="">{t("dsh.asItIs")}</option>}
              {choices.map((c) => (
                <option key={c.value} value={c.value}>
                  {c.label}
                </option>
              ))}
            </select>
          )}
          {takeover && options && options.permissions.length > 0 && (
            <select
              className="dsh-pick"
              aria-label={t("dsh.permission")}
              value={permission || options.currentPermission}
              onChange={(e) => ((reqId.current = ""), setPermission(e.target.value))}
            >
              {!options.currentPermission && <option value="">{t("dsh.asItIs")}</option>}
              {options.permissions.map((x) => (
                <option key={x.id} value={x.id} title={x.description}>
                  {x.name}
                </option>
              ))}
            </select>
          )}
          <span className="dsh-bar-note">
            {switchError || error ? (
              <span className="err" role="alert">
                {switchError || error}
              </span>
            ) : (
              notice && <span role="status">{notice}</span>
            )}
          </span>
          {takeover && running && (
            <button className="btn sm" onClick={interrupt} disabled={!b.ok}>
              {t("dsh.interrupt")}
            </button>
          )}
          <button className="dsh-send" onClick={send} disabled={off || busy || !text.trim() || !b.ok} title={t("dsh.send")} aria-label={t("dsh.send")}>
            <Icon name="arrowUp" />
          </button>
        </div>
      </div>
    </div>
  );
}
