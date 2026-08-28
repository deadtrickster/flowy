import { useSearchParams } from "react-router-dom";

import { VmShell } from "@/components/VmShell";

/**
 * ONE SHELL, IN A WINDOW OF ITS OWN.
 *
 * The operator: "also worth having something like 'open in a window' that is -
 * browser window". Not a second implementation of the panel - the same
 * component, with nothing else on the page.
 *
 * IT ADOPTS THE SAME SHELL, and that falls out of what is already built rather
 * than being arranged here. The session id is remembered in localStorage, which
 * is per ORIGIN and therefore shared with the window that opened this one, so
 * this panel mounts, finds the id for that project and slot, and reattaches.
 * The node already fans output to every reader of a session and accepts input
 * from any of them, so both windows show the same terminal and both can type -
 * `screen -x`, deliberately, and the same property OpenChamber calls multiple
 * simultaneous viewers.
 *
 * WHICH MEANS CLOSING THIS WINDOW ENDS NOTHING. Unmounting detaches; the
 * session keeps running and the panel that opened it keeps drawing.
 */
export function ShellWindow() {
  const [params] = useSearchParams();
  const project = params.get("project") ?? "";
  // A NUMBER OR NOTHING. A slot that is not a number would attach to NaN and
  // the frames would go nowhere with no error - so an unreadable one falls back
  // to 0, which is the slot every panel starts with.
  const asked = Number.parseInt(params.get("slot") ?? "", 10);
  const slot = Number.isInteger(asked) && asked >= 0 && asked <= 255 ? asked : 0;

  return (
    <div className="flex h-full min-h-0 flex-col p-3" data-shell-window={String(slot)}>
      <VmShell project={project} slot={slot} />
    </div>
  );
}

export default ShellWindow;
