import { VmShell } from "@/components/VmShell";
import { useCallback, useEffect, useRef, useState } from "react";

import { Badge } from "@/components/ui/badge";
import type { VM, VMProject } from "@/lib/api";
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
function ShellTabs({ project }: { project: string }) {
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
          <VmShell project={project} slot={slot} />
        </div>
      ))}
    </section>
  );
}

export function Vms() {
  const [projects, setProjects] = useState<VMProject[]>([]);
  const [vms, setVms] = useState<VM[]>([]);
  // The read either happened or it did not. `loaded` false draws "reading",
  // never "nothing running" - the same distinction the node protects one layer
  // down, and the one this page is most likely to lose by accident.
  const [loaded, setLoaded] = useState(false);
  const [failure, setFailure] = useState<{ status: number; why: string } | null>(null);
  const [project, setProject] = useState("");
  const [prompt, setPrompt] = useState("");
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
      <div className="p-6" data-vm-panel="" data-vm-state={state}>
        <h1 className="font-semibold text-base">VMs</h1>
        <p className="mt-2 max-w-2xl text-muted-foreground text-sm">
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
      </div>
    );
  }

  return (
    // h-full min-h-0 so the height main hands down reaches the terminal, and
    // overflow-y-auto so a short window still scrolls to the form rather than
    // clipping it - main is overflow-hidden, so without this the page loses its
    // bottom instead of scrolling.
    <div
      className="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-6"
      data-vm-panel=""
      data-vm-state="ok"
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
        <label className="flex flex-col gap-1 text-xs">
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

      {/* A SHELL, BESIDE THE SPAWN CONTROLS THAT WERE ALREADY HERE.
          The operator asked for "a run button which will bring fcvm with the
          shell relayed to the panel". This page already knew how to list
          projects, spawn over one and read a log; what it could not do is the
          interactive half, which is the whole of this row. Put here rather
          than on a new page because a second page listing the same projects
          with a different verb is two answers to "what can I run over". */}
      <ShellTabs project={project} />

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
    </div>
  );
}

export default Vms;
