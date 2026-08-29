import { ShellSessions } from "@/components/ShellSessions";
import { VmShell } from "@/components/VmShell";
import { useCallback, useEffect, useRef, useState } from "react";

import { Badge } from "@/components/ui/badge";
import type { VM, VMProject, VMTopRow } from "@/lib/api";
import { ApiError, api } from "@/lib/api";

/**
 * SPAWN AN AGENT INTO AN fc VM, FROM THE CONSOLE. 01M0G0KT52, the operator's
 * ask (3): "I want to be able to spawn agent right from flow - inside fc VM".
 *
 * The node has had every door this needs since api_vm.go landed - projects,
 * list, spawn, log, say, down - and `grep -rn 'api/vm' web/src` returned
 * nothing. So this is the console catching up to the node, which is the usual
 * direction here: before believing a feature is missing, look for a caller.
 *
 * WHY THE ERROR STATES GET MORE CODE THAN THE HAPPY PATH. api_vm.go is careful
 * to answer 503 rather than an empty list when the host has no firecode,
 * because "no VMs are running" and "this node cannot run VMs" are different
 * facts and a page that draws both as nothing tells the operator the second is
 * the first. That care is worth nothing if the last layer catches everything
 * into a blank panel, so this branches on status and says which one it is:
 *
 *   403  not the operator - the doors are operator-only on purpose
 *   503  this node cannot run VMs at all
 *   502  firecode was there and refused, with what it said
 *   ok, empty  nothing is running
 *
 * AND THE PRECEDENT IT IS AVOIDING, named because it is in this repository:
 * the repro panel is built, correct, and inert, because the node was started
 * without a flag nobody had heard of, and the console drew it as though it
 * worked. The operator's response was "i dont care about any runners". A
 * feature whose last mile is invisible is not delivered.
 */

/**
 * SEVERAL SHELLS, ONE SOCKET.
 *
 * 01M14HN1VX, the operator: "I want Shell tabs". lib/agentsocket owns one
 * connection for the document and routes frames by slot, so a strip is N
 * <VmShell slot={i}> and no socket code here - which is what the slot byte on
 * the wire is for. Opening a socket per tab would work and would waste it.
 *
 * CLOSING A TAB DETACHES; IT DOES NOT STOP. The wire keeps those apart on
 * purpose and so does this: unmounting a VmShell detaches its slot, the session
 * keeps running on the node, and reattach finds it. A close that stopped the
 * session would mean a stray click kills a build, which is the failure the
 * whole reattach change exists to prevent. Stopping is the shell's own control,
 * inside the panel, where it names the session it ends.
 *
 * A SLOT IS NEVER REUSED WHILE THE PAGE LIVES. Slots are handed out by a
 * counter rather than by index, because the node keys attachments by slot and a
 * closed tab's detach may still be in flight when the next one opens. Reusing
 * the number would attach a new terminal to a slot the node is still tearing
 * down.
 */
function ShellTabs({ project, mux = "" }: { project: string; mux?: string }) {
  const [tabs, setTabs] = useState<number[]>([0]);
  const [live, setLive] = useState(0);
  const next = useRef(1);

  const add = () => {
    // A byte on the wire. 256 terminals in one browser tab is not a limit
    // anybody reaches; refusing past it is better than wrapping to 0 and
    // stealing slot 0's session.
    if (next.current > 255) return;
    const slot = next.current++;
    setTabs((t) => [...t, slot]);
    setLive(slot);
  };

  const close = (slot: number) => {
    setTabs((t) => {
      const left = t.filter((s) => s !== slot);
      // Never leave the strip empty: a panel with no tabs has no way back to
      // one, and the reader would have to reload to get a shell.
      if (left.length === 0) {
        const fresh = next.current++;
        setLive(fresh);
        return [fresh];
      }
      setLive((cur) => (cur === slot ? left[left.length - 1] : cur));
      return left;
    });
  };

  return (
    <section className="flex min-h-0 flex-1 flex-col gap-2" data-shell-tabs={tabs.length}>
      <div className="flex flex-wrap items-center gap-1">
        {tabs.map((slot, i) => (
          <span key={slot} className="flex items-center">
            <button
              type="button"
              data-shell-tab={slot}
              data-shell-tab-live={slot === live ? "yes" : "no"}
              onClick={() => setLive(slot)}
              className={
                slot === live
                  ? "cursor-default rounded-l border border-primary bg-primary/10 px-2 py-0.5 font-mono text-primary text-xs"
                  : "cursor-pointer rounded-l border border-border px-2 py-0.5 font-mono text-muted-foreground text-xs hover:bg-accent"
              }
            >
              shell {i + 1}
            </button>
            <button
              type="button"
              data-shell-tab-close={slot}
              title="close this tab - the shell keeps running and reattaches"
              onClick={() => close(slot)}
              className="cursor-pointer rounded-r border border-border border-l-0 px-1 py-0.5 font-mono text-muted-foreground text-xs hover:bg-accent"
            >
              x
            </button>
          </span>
        ))}
        <button
          type="button"
          data-shell-tab-add
          onClick={add}
          className="cursor-pointer rounded border border-border px-2 py-0.5 font-mono text-muted-foreground text-xs hover:bg-accent"
        >
          +
        </button>
      </div>
      {/*
        EVERY TAB STAYS MOUNTED, and the ones not being read are hidden rather
        than unmounted. Unmounting would detach the slot - which is what closing
        means - so switching tabs would silently drop the session the reader
        just walked away from for a second. Hidden keeps the terminal, its
        scrollback and its attachment exactly as they were.
      */}
      {tabs.map((slot) => (
        // min-h-0 flex-1 on the LIVE pane only: a hidden one must not take a
        // share of the column, and `hidden` alone does not stop a flex item
        // from being laid out as one.
        <div
          key={slot}
          hidden={slot !== live}
          data-shell-pane={slot}
          className={slot === live ? "flex min-h-0 flex-1 flex-col" : undefined}
        >
          <VmShell project={project} slot={slot} mux={mux} />
        </div>
      ))}
    </section>
  );
}

/**
 * A reading, or a dash.
 *
 * null from fctop means THIS WAS NOT KNOWN - the guest did not answer, or was
 * never asked - and it arrives on the same field that carries a real number.
 * Rendering it as 0, or as an empty cell, makes "no memory in use" and "I could
 * not ask" the same picture, which is the whole thing the STATUS column beside
 * it exists to keep apart.
 */
function reading<T>(value: T | null | undefined, draw: (v: T) => string): string {
  return value == null ? "-" : draw(value);
}

/** Seconds as something a person reads at a glance, the way fctop prints it. */
function forHowLong(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h`;
  return `${Math.round(seconds / 86400)}d`;
}

export function Vms() {
  const [projects, setProjects] = useState<VMProject[]>([]);
  const [vms, setVms] = useState<VM[]>([]);
  // THE PROBED FRAME, AND WHY IT IS KEPT APART FROM `vms`. /api/vm/list is the
  // host's view and always answers; this asks each guest and needs fctop on the
  // node's PATH. A node with firecode and no fctop lists its VMs and cannot say
  // how they are, which is a real state and not an error - so the readings are
  // their own value with their own failure, and neither stands in for the other.
  const [top, setTop] = useState<VMTopRow[] | null>(null);
  const [topWhy, setTopWhy] = useState("");
  // The read either happened or it did not. `loaded` false draws "reading",
  // never "nothing running" - the same distinction the node protects one layer
  // down, and the one this page is most likely to lose by accident.
  const [loaded, setLoaded] = useState(false);
  const [failure, setFailure] = useState<{ status: number; why: string } | null>(null);
  const [project, setProject] = useState("");
  const [prompt, setPrompt] = useState("");
  // WHICH PANE. Not in the URL yet: /vms is one page and the pane is a view of
  // it, so a link to /vms should land where you left off rather than on a
  // pane somebody else chose. If that turns out to be wrong it wants a route,
  // not a query parameter.
  const [pane, setPane] = useState<"agents" | "shells">("shells");
  // WHICH SESSION THE SHELLS PANE IS SHOWING, empty for the project's own. Set
  // by picking one out of the list, including sessions flowy never started.
  const [mux, setMux] = useState("");
  const [busy, setBusy] = useState("");
  const [opened, setOpened] = useState<string | null>(null);
  const [log, setLog] = useState("");
  const [say, setSay] = useState("");
  const [said, setSaid] = useState("");
  const read = useRef(0);

  const refresh = useCallback(async () => {
    const mine = ++read.current;
    try {
      const [reg, running] = await Promise.all([api.vmProjects(), api.vmList()]);
      if (mine !== read.current) return;
      // SEPARATELY, AND ALLOWED TO FAIL ON ITS OWN. Folding this into the
      // Promise.all above would make a node without fctop draw the whole page
      // as broken, when what it cannot do is one column.
      void api
        .vmTop()
        .then((frame) => {
          if (mine !== read.current) return;
          setTop(frame.vms ?? []);
          setTopWhy("");
        })
        .catch((err) => {
          if (mine !== read.current) return;
          setTop(null);
          setTopWhy(err instanceof Error ? err.message : String(err));
        });
      // Only projects whose directory still exists. The node resolves a spawn
      // against the same registry and refuses the rest, so offering them would
      // be a button that is refused on click.
      setProjects((reg.projects ?? []).filter((p) => p.exists));
      setVms(running.vms ?? []);
      setFailure(null);
      setLoaded(true);
    } catch (err) {
      if (mine !== read.current) return;
      const status = err instanceof ApiError ? err.status : 0;
      setFailure({ status, why: err instanceof Error ? err.message : String(err) });
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    void refresh();
    // Ten seconds. A spawn answers 202 the moment the process starts and the
    // VM appears in the list a beat later, so a page with no refresh would
    // show the operator nothing happening after the one action that did.
    const timer = setInterval(() => void refresh(), 10_000);
    return () => clearInterval(timer);
  }, [refresh]);

  const spawn = async () => {
    if (!project) return;
    setBusy("spawn");
    try {
      await api.vmSpawn(project, prompt);
      setPrompt("");
      await refresh();
    } catch (err) {
      setFailure({
        status: err instanceof ApiError ? err.status : 0,
        why: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setBusy("");
    }
  };

  const openLog = async (name: string) => {
    setOpened(name);
    setLog("");
    setSaid("");
    try {
      setLog(await api.vmLog(name));
    } catch (err) {
      setLog(err instanceof Error ? err.message : String(err));
    }
  };

  const sendTurn = async () => {
    if (!opened || !say.trim()) return;
    setBusy("say");
    try {
      await api.vmSay(opened, say);
      setSay("");
      // The turn is in the guest's log, not in this page's state, so the log is
      // re-read rather than optimistically appended - an echo the node never
      // confirmed is the console inventing evidence.
      setSaid("sent - it appears in the log when the agent gets to it");
      await openLogQuietly(opened);
    } catch (err) {
      setSaid(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  };

  const openLogQuietly = async (name: string) => {
    try {
      setLog(await api.vmLog(name));
    } catch {
      /* the log is a read; a failed one leaves what was there */
    }
  };

  const down = async (name: string) => {
    setBusy(`down:${name}`);
    try {
      await api.vmDown(name);
      if (opened === name) setOpened(null);
      await refresh();
    } catch (err) {
      setFailure({
        status: err instanceof ApiError ? err.status : 0,
        why: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setBusy("");
    }
  };

  if (!loaded) {
    // NAMED, like the others. This used to carry data-vm-panel and no state,
    // which made "still reading" indistinguishable from "rendered with no
    // state" to anything looking at the attribute - and the node is allowed
    // twenty seconds for `firecode ps`, so this branch is on screen for
    // longer than a reader, or a check, would assume.
    return (
      <div className="p-6" data-vm-panel="" data-vm-state="reading">
        <p className="text-muted-foreground text-sm">reading the host…</p>
      </div>
    );
  }

  // EACH REFUSAL SAYS WHICH ONE IT IS. The status is what separates them, and
  // the sentence the node sent is shown rather than replaced - it names the
  // binary and the machine, which is the actionable half.
  if (failure) {
    const state =
      failure.status === 403 ? "forbidden" : failure.status === 503 ? "unavailable" : "refused";
    return (
      // A REFUSAL IS A PAGE, NOT A HEADLINE ON A BLACK FIELD. The operator, on
      // a screenshot: "/vms for a non-operator is a headline and one sentence
      // on a full black page. The refusal is right and the emptiness is not -
      // 95% of the viewport says nothing."
      //
      // Both halves of that are kept. The refusal still says WHICH refusal it
      // is and still shows the node's own sentence, because that names the
      // binary and the machine and is the actionable half. What is added is
      // what the page WOULD show, so somebody who cannot see it still learns
      // what it is and what it would take - an empty state that teaches rather
      // than one that merely declines.
      <div className="p-6" data-vm-panel="" data-vm-state={state}>
        <div className="mx-auto max-w-2xl">
          <h1 className="font-semibold text-base">VMs</h1>
          <p className="mt-2 text-muted-foreground text-sm" data-vm-refusal="">
            {state === "forbidden" ? (
              <>
                spawning a VM is the operator's, and this token is not the operator's. That is a
                permission, not a fault — nothing here is broken.
              </>
            ) : state === "unavailable" ? (
              <>this node cannot run VMs: {failure.why}. This is NOT the same as none running.</>
            ) : (
              <>firecode answered, and refused: {failure.why}</>
            )}
          </p>

          <div className="mt-6 rounded-lg border border-border bg-card p-4" data-vm-would-show="">
            <p className="font-medium text-sm">what this page shows</p>
            <dl className="mt-3 flex flex-col gap-3 text-sm">
              <div>
                <dt className="font-medium">agents</dt>
                <dd className="text-muted-foreground">
                  every VM this host is running, with the readings fctop takes: status as a word,
                  how long since it last wrote, memory, uptime, load, and which agent and project it
                  belongs to.
                </dd>
              </div>
              <div>
                <dt className="font-medium">shells</dt>
                <dd className="text-muted-foreground">
                  a terminal in a project's byobu session — the same session ssh and Emacs attach
                  to, so a shell opened here is one you can pick up from either.
                </dd>
              </div>
            </dl>
            <p className="mt-4 text-muted-foreground text-xs">
              {state === "forbidden"
                ? "both doors are operator-only. An operator token is what opens them; nothing here degrades to a partial view, because a partial fleet reads as a whole one."
                : "the doors are there and this node cannot answer them. Nothing on this page is a reading of the fleet."}
            </p>
          </div>
        </div>
      </div>
    );
  }

  return (
    // h-full min-h-0 because the height main hands down has to reach the
    // terminal, and NOT overflow-y-auto here: each pane scrolls itself, so a
    // scroll on the page would put a second scrollbar around a terminal that
    // is already exactly as tall as its pane.
    <div className="flex h-full min-h-0 flex-col p-6" data-vm-panel="" data-vm-state="ok">
      {/*
        TWO PANES, NOT ONE PAGE WITH A TERMINAL AT THE BOTTOM.

        The shell strip was the last thing on a page that begins with a header,
        a project picker, a spawn form and the list of running VMs. The operator,
        with three shells open: "with three listed shells tabs already pushed to
        the bottom and squished."

        That is what a scrolling page does to the one thing on it that wants a
        definite height. So the page is now the tabs: whichever is chosen takes
        the WHOLE panel, and the terminal's container is the panel rather than
        whatever is left under four other sections.
      */}
      {/*
        THE PROJECT PICKER BELONGS TO THE PAGE, not to one pane. Both panes are
        about the same project - the agents pane spawns over it, the shells pane
        opens a shell in it - and a picker inside one of them means the other
        cannot be used without visiting the first, or means two pickers that can
        disagree about what you are looking at.

        A LIST, NOT FREE TEXT. The node resolves the name against firecode's
        registry and refuses anything else: typing a path here would be a field
        whose only outcome is a refusal, and a caller that can name a directory
        could otherwise pack any directory into a VM with network.
      */}
      <label className="flex items-center gap-2 pb-2 text-xs">
        <span className="text-muted-foreground">project</span>
        <select
          data-vm-project=""
          className="rounded border border-border bg-transparent px-2 py-1 text-sm"
          value={project}
          onChange={(e) => setProject(e.target.value)}
        >
          <option value="">choose one</option>
          {projects.map((p) => (
            <option key={p.name} value={p.name}>
              {p.name}
            </option>
          ))}
        </select>
      </label>
      <div className="flex items-center gap-1 border-border border-b pb-2" data-vm-tabs="">
        {(["agents", "shells"] as const).map((name) => (
          <button
            key={name}
            type="button"
            data-vm-tab={name}
            data-vm-tab-live={pane === name ? "yes" : "no"}
            onClick={() => setPane(name)}
            className={
              pane === name
                ? "cursor-default rounded border border-primary bg-primary/10 px-3 py-1 font-mono text-primary text-xs"
                : "cursor-pointer rounded border border-border px-3 py-1 font-mono text-muted-foreground text-xs hover:bg-accent"
            }
          >
            {name}
          </button>
        ))}
      </div>

      {/*
        BOTH PANES STAY MOUNTED and the one not being read is hidden. Unmounting
        the shells pane would unmount every VmShell in it, which detaches every
        slot - so glancing at the agents list would drop the sessions somebody
        walked away from for a second. Same rule the tab strip inside it already
        follows, for the same reason.
      */}
      <section
        hidden={pane !== "agents"}
        data-vm-pane="agents"
        className={
          pane === "agents" ? "flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto pt-4" : undefined
        }
      >
        <header className="flex flex-col gap-1">
          <h1 className="font-semibold text-base">VMs</h1>
          <p className="text-muted-foreground text-xs">
            an agent in a firecracker VM over a copy of a project. It runs unattended when given a
            first turn, and waits when not.
          </p>
        </header>

        <section className="flex flex-wrap items-end gap-2 border-border border-b pb-4">
          {/*
          A LIST, NOT FREE TEXT. The node resolves the name against firecode's
          registry and refuses anything else - typing a path here would be a
          field whose only outcome is a refusal, and a caller that can name a
          directory could otherwise pack any directory into a VM with network.
        */}
          <label className="flex min-w-64 flex-1 flex-col gap-1 text-xs">
            <span className="text-muted-foreground">first turn (optional)</span>
            <input
              data-vm-prompt=""
              className="rounded border border-border bg-transparent px-2 py-1 text-sm"
              placeholder="what the agent should do"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
            />
          </label>
          <button
            type="button"
            data-vm-spawn=""
            disabled={!project || busy === "spawn"}
            onClick={() => void spawn()}
            className="rounded border border-border px-3 py-1 text-sm disabled:opacity-50"
          >
            {busy === "spawn" ? "starting…" : "spawn"}
          </button>
        </section>

        {/*
          THE READINGS, WHICH IS THE HALF /api/vm/list CANNOT ANSWER.

          The operator: "agents tab then essentially mirror fc-top". fctop's
          columns, and its STATUS word rendered as a word - not folded into a
          colour, because "STALE 42s" carries a number that a colour cannot, and
          not dropped, because a row whose guest never answered would otherwise
          show zeros indistinguishable from a guest that answered zero.

          Every reading is drawn through `reading`, which prints a dash for null.
          Null here means NOT KNOWN, and a dash is the only honest glyph for it.
        */}
        {topWhy ? (
          <p data-vm-top-why="" className="text-muted-foreground text-xs">
            no readings: {topWhy}
          </p>
        ) : top === null ? (
          <p data-vm-top-reading="" className="text-muted-foreground text-xs">
            reading how the VMs are…
          </p>
        ) : top.length > 0 ? (
          <div className="overflow-x-auto" data-vm-top="">
            <table className="w-full text-left text-xs">
              <thead className="text-muted-foreground">
                <tr>
                  {["name", "status", "last-out", "memory", "up", "load", "agent", "project"].map(
                    (h) => (
                      <th key={h} className="py-1 pr-4 font-normal">
                        {h}
                      </th>
                    ),
                  )}
                </tr>
              </thead>
              <tbody className="font-mono">
                {top.map((row) => (
                  <tr
                    key={row.run_id}
                    data-vm-top-row={row.name}
                    className="border-border-soft border-t"
                  >
                    <td className="py-1 pr-4">{row.name}</td>
                    <td className="py-1 pr-4" data-vm-top-status={row.status}>
                      {row.status}
                    </td>
                    <td className="py-1 pr-4">
                      {reading(row.last_output_s, (n) => `${n}s`)}
                      {row.last_output_note ? "!" : ""}
                    </td>
                    <td className="py-1 pr-4">
                      {row.mem_used_mb == null || row.mem_total_mb == null
                        ? "-"
                        : `${row.mem_used_mb}/${row.mem_total_mb}M`}
                    </td>
                    <td className="py-1 pr-4">{reading(row.uptime_s, forHowLong)}</td>
                    <td className="py-1 pr-4">
                      {reading(row.load, (l) => String(l).split(" ")[0])}
                    </td>
                    <td className="py-1 pr-4">{reading(row.agent, String)}</td>
                    <td className="py-1 pr-4 text-muted-foreground">{row.project}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}

        {vms.length === 0 ? (
          // NOTHING RUNNING, said as itself. This is the branch that has to be
          // reachable only when the host answered and had nothing - every other
          // reason for an empty screen is handled above.
          <p data-vm-empty="" className="text-muted-foreground text-sm">
            no VMs are running. This host answered, and had none — spawn one above.
          </p>
        ) : (
          <ul className="flex flex-col">
            {vms.map((vm) => (
              <li
                key={vm.id}
                data-vm-row={vm.name}
                className="flex flex-col gap-1 border-border-soft border-b py-3"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    data-vm-open={vm.name}
                    onClick={() => void openLog(vm.name)}
                    className="font-medium text-sm hover:underline"
                  >
                    {vm.name}
                  </button>
                  <Badge variant="secondary">{vm.backend}</Badge>
                  {/*
                  SAID WITH ITS LIMIT. `ps` does not probe the guest - that is a
                  25s timeout each - so this is how long ago the run last
                  printed, not a heartbeat. Drawing it as "alive" would be a
                  claim the host never made.
                */}
                  <span className="text-muted-foreground text-xs">
                    last printed {vm.last_output_s}s ago
                    {vm.probed ? null : " (not probed)"}
                  </span>
                  <button
                    type="button"
                    data-vm-down={vm.name}
                    disabled={busy === `down:${vm.name}`}
                    onClick={() => void down(vm.name)}
                    className="ml-auto rounded border border-border px-2 py-0.5 text-xs disabled:opacity-50"
                  >
                    {busy === `down:${vm.name}` ? "stopping…" : "down"}
                  </button>
                </div>
                <span className="text-muted-foreground text-xs">
                  <code className="text-xs">{vm.project}</code>
                </span>
              </li>
            ))}
          </ul>
        )}

        {opened ? (
          <section className="flex flex-col gap-2" data-vm-console={opened}>
            <h2 className="font-semibold text-sm">{opened}</h2>
            <pre
              data-vm-log=""
              className="max-h-96 overflow-auto rounded border border-border p-3 text-xs"
            >
              {log || "(nothing printed yet)"}
            </pre>
            <div className="flex flex-wrap items-end gap-2">
              <input
                data-vm-say=""
                className="min-w-64 flex-1 rounded border border-border bg-transparent px-2 py-1 text-sm"
                placeholder="another turn"
                value={say}
                onChange={(e) => setSay(e.target.value)}
              />
              <button
                type="button"
                data-vm-send=""
                disabled={!say.trim() || busy === "say"}
                onClick={() => void sendTurn()}
                className="rounded border border-border px-3 py-1 text-sm disabled:opacity-50"
              >
                {busy === "say" ? "sending…" : "say"}
              </button>
            </div>
            {said ? (
              <p data-vm-said="" className="text-muted-foreground text-xs">
                {said}
              </p>
            ) : null}
          </section>
        ) : null}
      </section>

      {/*
        THE SHELLS PANE, and it is the whole panel. The project it runs over is
        the one picked on the agents pane - one answer to "what can I run over"
        rather than two pickers that can disagree.
      */}
      <section
        hidden={pane !== "shells"}
        data-vm-pane="shells"
        className={pane === "shells" ? "flex min-h-0 flex-1 flex-col gap-2 pt-4" : undefined}
      >
        {/*
          WHAT IS ALREADY RUNNING, ABOVE THE TERMINAL. The operator: "so your
          stuff is just byobu management." A terminal with no idea what else is
          on the host is the half of that we already had.

          A <details> so it is one line when you do not want it: the terminal is
          what the pane is for, and a list that pushed it down would be the
          complaint that started this - "shells tabs already pushed to the
          bottom and squished".
        */}
        <details data-shell-sessions-fold="">
          <summary className="cursor-pointer text-muted-foreground text-xs hover:text-foreground">
            sessions on this host
          </summary>
          <div className="pt-2">
            <ShellSessions project={project} onOpen={setMux} />
          </div>
        </details>
        {/*
          THE KEY IS THE SESSION, so choosing a different one from the list
          REMOUNTS the strip rather than leaving terminals attached to the
          session they were opened on. Without it, picking a second session
          would change what new tabs join and leave the open ones lying about
          which session they are showing.
        */}
        <ShellTabs key={mux || project} project={project} mux={mux} />
      </section>
    </div>
  );
}

export default Vms;
