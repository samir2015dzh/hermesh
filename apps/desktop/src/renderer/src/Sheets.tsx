import { useLayoutEffect } from "react";
import { AddDeviceSheet, AddMachineSheet, AddProjectSheet } from "./sheets/Add";
import { ConfirmSheet } from "./sheets/Confirm";
import { ContactsSheet } from "./sheets/Contacts";
import { EraseSheet } from "./sheets/Erase";
import { NewGrantSheet } from "./sheets/NewGrant";
import { NewSessionSheet } from "./sheets/NewSession";
import { ScopeSheet } from "./sheets/Scope";
import { WakeSheet } from "./sheets/Wake";
import { useLeaving } from "./components";
import { useStore, type Sheet } from "./store";
import "./sheets/sheets.css";

// Sheets over the window: connect an agent, start a session, add machine/project/device, scope, an agent's wake, confirm, erase everything, turning Her off. One at a time, in st.sheet (the app
// menu's commands open them through commands.ts).

const widths: Record<Sheet["kind"], number> = { newGrant: 620, addMachine: 560, addProject: 540, newSession: 600, contacts: 600, addDevice: 520, scope: 560, wake: 480, confirm: 440, erase: 520 };

export function SheetHost() {
  const st = useStore();
  const { sheet, set } = st;
  // Before the sheet is painted: an Escape pressed as soon as it shows closes it.
  useLayoutEffect(() => {
    if (!sheet) return;
    // Erasing everything is not left half way: its sheet has its own Cancel.
    const esc = (e: KeyboardEvent) => e.key === "Escape" && sheet.kind !== "erase" && set({ sheet: null });
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, [sheet, set]);
  // Closed, it fades out as it was.
  const { shown, leaving } = useLeaving(sheet);
  if (!shown) return null;
  return (
    <>
      <div className={`sheet-scrim ${leaving ? "leaving" : ""}`} />
      <div className={`sheet ${leaving ? "leaving" : ""}`} inert={leaving} role="dialog" aria-modal="true" aria-labelledby="sheet-title" style={{ width: widths[shown.kind] }}>
        <Body key={shown.kind} sheet={shown} />
      </div>
    </>
  );
}

function Body({ sheet }: { sheet: Sheet }) {
  switch (sheet.kind) {
    case "newGrant":
      return <NewGrantSheet />;
    case "addMachine":
      return <AddMachineSheet />;
    case "addProject":
      return <AddProjectSheet machineId={sheet.machineId} agent={sheet.agent} />;
    case "newSession":
      return <NewSessionSheet machineId={sheet.machineId} agent={sheet.agent} />;
    case "contacts":
      return <ContactsSheet />;
    case "addDevice":
      return <AddDeviceSheet />;
    case "scope":
      return <ScopeSheet clientId={sheet.clientId} renew={sheet.renew} />;
    case "wake":
      return <WakeSheet sheet={sheet} />;
    case "confirm":
      return <ConfirmSheet sheet={sheet} />;
    case "erase":
      return <EraseSheet />;
  }
}
