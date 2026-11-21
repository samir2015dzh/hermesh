import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Device } from "@aide/proto/local";
import { platformTitle } from "@aide/core";
import { local } from "../api";
import { herDataLeft } from "../herLegacy";
import { Icon } from "../icons";
import { errorText, useStore } from "../store";
import { copy, More, SRow, type Ask } from "./common";

/**
 * Account & Devices: who this computer belongs to and where it connects,
 * the account every device, machine and agent logs in with (docs/accounts.md
 * §4), the devices logged in (any signs out another), and leaving.
 * Addresses and commands are folded under Advanced.
 */
export function Account({ ask }: { ask: (a: Ask) => void }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const s = st.status;
  if (s?.machineOnly) return <MachineAccount />;
  const user = window.aide?.userName || "—";
  const relay = s?.relayUrl ?? "";
  const host = URL.canParse(relay) ? new URL(relay).host : relay;
  const account = s?.account ?? "";
  const removeDevice = (d: Device) => {
    // Its own machine goes with it, and data Her left there, from before Aide dropped her, can go too.
    const m = st.machines.find((x) => x.machineId === d.machineId);
    st.set({
      sheet: {
        kind: "confirm",
        title: t("removeDeviceTitle", { name: d.label }),
        message: t("account.removeDeviceMessage"),
        button: t("removeButton"),
        run: (eraseHer) => local.removeDevice({ keyId: d.keyId, eraseHer }),
        done: t("removed", { name: d.label }),
        herData: m && herDataLeft(m) ? { name: m.machineName, offline: !m.online } : undefined,
      },
    });
  };
  const leave = () =>
    s?.removed
      ? st.set({
          sheet: {
            kind: "confirm",
            title: t("account.signOutTitle"),
            message: t("account.signOutRemovedMessage"),
            button: t("account.signOutButton"),
            run: async () => {
              await local.leave({});
              window.close();
            },
          },
        })
      : ask({
          title: t("account.signOutTitle"),
          message: t("account.signOutFirstMessage"),
        });
  return (
    <div className="form">
      <div className="dhero">
        <span className="av xl me" aria-hidden="true">{user.slice(0, 1).toUpperCase()}</span>
        <div>
          <b>{user}</b>
          <span>
            {t("account.ownServer", { host: host || "—" })}
            {s && !s.relayConnected && <span className="warn"> · {t("account.notConnected")}</span>}
          </span>
        </div>
      </div>
      <div>
        <div className="fhead">{t("account.devices")}</div>
        <div className="fgroup" role="list" aria-label={t("account.devices")}>
          {st.devices.map((d) => (
            <div className="frow" role="listitem" key={d.keyId}>
              <Icon name="laptop" />
              <div className="ft">
                <div className="fn">
                  {d.label}
                  {d.thisDevice && <span className="faint"> {t("thisMac")}</span>}
                </div>
                <div className="fdesc">
                  {[platformTitle(d.platform), d.machineId ? t("account.alsoMachine", { status: t(d.machineOnline ? "online" : "offline") }) : ""].filter(Boolean).join(" · ")}
                </div>
              </div>
              {!d.thisDevice && (
                <button className="btn sm danger" onClick={() => removeDevice(d)}>{t("account.signOutDevice")}</button>
              )}
            </div>
          ))}
          <div className="frow">
            <button className="link" onClick={() => st.set({ sheet: { kind: "addDevice" } })}>{t("account.addDevice")}</button>
          </div>
        </div>
        <p className="set-note">{t("account.devicesNote")}</p>
        {s && <AccountLogin />}
      </div>
      <More label={t("account.advanced")}>
        <div className="fgroup">
          <div className="frow">
            <span className="fl">{t("account.relay")}</span>
            <span className="fv">
              {host || "—"} <i className={`dotc ${s?.relayConnected ? "on" : "warn"}`} />
              {t(s?.relayConnected ? "account.connected" : "account.notConnected")}
            </span>
          </div>
          <CopyRow k={t("account.ownerId")} text={s?.ownerId ?? ""} />
          {relay && account && <CopyRow k={t("account.addMachine")} text={`aided login ${relay} --account ${account}`} />}
          {relay && account && <CopyRow k={t("account.agentLogin")} text={`aide auth login ${relay} --account ${account}`} />}
          {s?.mcpUrl && <CopyRow k={t("account.mcpUrl")} text={s.mcpUrl} />}
        </div>
      </More>
      <div className="set-acts">
        {s && !s.removed && (
          <button className="btn danger" onClick={() => st.set({ sheet: { kind: "erase" } })}>{t("account.eraseAll")}</button>
        )}
        <button className="btn danger push" onClick={leave}>{t("account.signOut")}</button>
      </div>
    </div>
  );
}

/**
 * A machine only's account: whose it is and where it connects, folded its
 * owner id; it has no devices to list. It can become one of the owner's
 * devices too (logging in again, in the main window), or leave.
 */
function MachineAccount() {
  const { t } = useTranslation("settings");
  const { t: f } = useTranslation("firstrun");
  const st = useStore();
  const s = st.status!;
  const user = window.aide?.userName || "—";
  const host = URL.canParse(s.relayUrl) ? new URL(s.relayUrl).host : s.relayUrl;
  const leave = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: f("machineOnly.leaveTitle"),
        message: f("machineOnly.leaveMessage"),
        button: f("machineOnly.leaveButton"),
        run: async () => {
          await local.leave({});
          window.close();
        },
      },
    });
  return (
    <div className="form">
      <div className="dhero">
        <span className="av xl me" aria-hidden="true">{user.slice(0, 1).toUpperCase()}</span>
        <div>
          <b>{user}</b>
          <span>
            {t("account.ownServer", { host: host || "—" })}
            {!s.relayConnected && <span className="warn"> · {t("account.notConnected")}</span>}
          </span>
        </div>
      </div>
      <p className="set-note">{t("account.machineOnlyNote")}</p>
      <More label={t("account.advanced")}>
        <div className="fgroup">
          <div className="frow">
            <span className="fl">{t("account.relay")}</span>
            <span className="fv">
              {host || "—"} <i className={`dotc ${s.relayConnected ? "on" : "warn"}`} />
              {t(s.relayConnected ? "account.connected" : "account.notConnected")}
            </span>
          </div>
          <CopyRow k={t("account.ownerId")} text={s.ownerId} />
        </div>
      </More>
      <div className="set-acts">
        <button className="btn" onClick={() => (window.aide?.showUpgrade(), window.close())}>{f("machineOnly.upgrade")}</button>
        <button className="btn danger push" onClick={leave}>{f("machineOnly.leave")}</button>
      </div>
    </div>
  );
}

type LoginForm = { kind: "change" | "off" | "on" | "convert"; current: string; account: string; password: string; again: string };

/**
 * The account (docs/accounts.md §4): its name and password change with the
 * current password, and a new one signs out every other device and every
 * agent; logging in can be turned off (and on again). A password login from
 * before accounts is converted here, with its password.
 */
function AccountLogin() {
  const { t } = useTranslation("settings");
  const st = useStore();
  const [info, setInfo] = useState<{ account: string; server: string; convert: boolean; off: boolean }>();
  const [form, setForm] = useState<LoginForm | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = () => local.getLogin({}).then((r) => setInfo({ account: r.account, server: r.server, convert: r.convert, off: r.off }), () => {});
  useEffect(() => {
    load();
  }, []);
  if (!info) return null;
  const open = (kind: LoginForm["kind"]) => (setError(""), setForm({ kind, current: "", account: info.account, password: "", again: "" }));
  const save = async () => {
    if (!form) return;
    const change = form.kind === "change";
    if (change && form.password !== form.again) return setError(t("account.login.mismatch"));
    setBusy(true);
    setError("");
    try {
      const r = await local.setLogin({
        currentPassword: form.current,
        account: change && form.account.trim() !== info.account ? form.account.trim() : "",
        newPassword: change ? form.password : "",
        off: form.kind === "off" || (form.kind !== "on" && info.off),
      });
      setForm(null);
      st.show(
        form.kind === "off" ? t("account.login.offDone")
        : form.kind === "on" ? t("account.login.onDone")
        : form.kind === "convert" ? t("account.login.converted")
        : r.signedOut > 0 ? t("account.login.changed", { count: r.signedOut })
        : t("account.login.saved"),
      );
      await load();
      st.refresh();
    } catch (e) {
      setError(errorText(e));
    }
    setBusy(false);
  };
  const fields: (keyof Omit<LoginForm, "kind">)[] = form?.kind === "change" ? ["current", "account", "password", "again"] : ["current"];
  return (
    <div>
      <div className="fhead">{t("account.login.title")}</div>
      <div className="fgroup">
        {!form && (
          <SRow
            title={info.convert ? t("account.login.convertTitle") : t("account.login.account", { account: info.account })}
            desc={info.convert ? t("account.login.convertDesc") : info.off ? t("account.login.offDesc") : t("account.login.onDesc", { server: info.server })}
          >
            {info.convert ? (
              <button className="btn sm primary" onClick={() => open("convert")}>
                {t("account.login.convert")}
              </button>
            ) : (
              <>
                <button className="btn sm" onClick={() => open("change")}>
                  {t("account.login.change")}
                </button>
                <button className={`btn sm ${info.off ? "" : "danger"}`} onClick={() => open(info.off ? "on" : "off")}>
                  {info.off ? t("account.login.turnOn") : t("account.login.turnOff")}
                </button>
              </>
            )}
          </SRow>
        )}
        {form && (
          <form className="pw-form" onSubmit={(e) => (e.preventDefault(), !busy && save())}>
            {fields.map((k) => (
              <div className="frow" key={k}>
                <label className="fl" htmlFor={`pw-${k}`}>
                  {t(`account.login.field.${k}`)}
                </label>
                <span className="fv">
                  <input
                    id={`pw-${k}`}
                    className="field"
                    type={k === "account" ? "text" : "password"}
                    autoComplete={k === "account" ? "username" : k === "current" ? "current-password" : "new-password"}
                    value={form[k]}
                    autoFocus={k === "current"}
                    disabled={busy}
                    onChange={(e) => (setError(""), setForm({ ...form, [k]: e.target.value }))}
                  />
                </span>
              </div>
            ))}
            <div className="frow">
              <span className="fdesc">{error ? <span className="warn">{error}</span> : t(`account.login.${form.kind}Note`)}</span>
              <span className="fv">
                <button type="button" className="btn sm" disabled={busy} onClick={() => setForm(null)}>
                  {t("cancel")}
                </button>
                <button type="submit" className={`btn sm ${form.kind === "off" ? "danger" : "primary"}`} disabled={busy || !form.current || (form.kind === "change" && !form.account.trim())}>
                  {t(`account.login.${form.kind}Button`)}
                </button>
              </span>
            </div>
          </form>
        )}
      </div>
      <p className="set-note">{t("account.login.cost")}</p>
    </div>
  );
}

function CopyRow({ k, text }: { k: string; text: string }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  return (
    <div className="frow">
      <span className="fl">{k}</span>
      <span className="fv">
        <span className="mono sel-text cut">{text || "—"}</span>
        <button className="btn sm" aria-label={t("copyLabel", { label: k })} disabled={!text} onClick={() => copy(st, text)}>
          <Icon name="copy" />
          {t("copy")}
        </button>
      </span>
    </div>
  );
}
