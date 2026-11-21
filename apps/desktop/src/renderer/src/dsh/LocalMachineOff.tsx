import { useState } from "react";
import { useTranslation } from "react-i18next";
import { local } from "../api";
import { localMachineOff } from "../localMachine";
import { useStore } from "../store";

/**
 * On the pages that list the sessions on the owner's machines (DSH's own, the Claude app's): this computer
 * is not turned on as a machine, so its sessions are not here, with the button that turns it on, the same
 * switch as Settings › Machines. `done` reads the list again once it is on.
 */
export function LocalMachineOff({ done }: { done: () => void }) {
  const { t } = useTranslation("common");
  const st = useStore();
  const [busy, setBusy] = useState(false);
  if (!localMachineOff(st.status)) return null;
  const turnOn = async () => {
    setBusy(true);
    const ok = await st.run(() => local.setLocalMachine({ enabled: true, machineName: st.status?.localMachineName ?? "" }));
    setBusy(false);
    if (ok) done();
  };
  return (
    <div className="dsh-local-off" role="status">
      <p>{t("dsh.localOff")}</p>
      <button className="btn" disabled={busy} onClick={() => void turnOn()}>
        {t("dsh.localOffTurnOn")}
      </button>
    </div>
  );
}
