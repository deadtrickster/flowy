import { useCallback, useEffect, useState } from "react";

import { ApiError, type ShellSession, api } from "@/lib/api";

/**
 * WHAT IS RUNNING ON THIS HOST, AND WHOSE IT IS.
 *
 * The operator: "per project byobu session i can attach to just over ssh, so
 * your stuff is just byobu management." This is the management: the sessions
 * the host holds, what is in each, and how to reach one from a terminal that is
 * not this browser.
 *
 * IT LISTS SESSIONS NOBODY HERE STARTED, on purpose. projectile/duckdb and
 * projectile/orioledb belong to their editor, and the whole point of the
 * convention is that the panel joins the same ones. A list of only what flowy
 * opened would be a list of the wrong thing.
 *
 * AND IT SHOWS THE ATTACH COMMAND, because the answer to "how do I get at this
 * from my laptop" is a line somebody types into ssh, not a button in a console
 * they may not have open.
 */
export function ShellSessions({ project }: { project: string }) {
  const [sessions, setSessions] = useState<ShellSession[] | null>(null);
  // WHY THERE IS NOTHING, kept apart from HAVING nothing. A host with no
  // multiplexer answers 503 and a host with no sessions answers an empty list,
  // and those want different sentences on screen.
  const [why, setWhy] = useState<{ status: number; text: string } | null>(null);

  const read = useCallback(async () => {
    try {
      const answer = await api.shellSessions();
      setSessions(answer.sessions ?? []);
      setWhy(null);
    } catch (err) {
      setSessions(null);
      setWhy({
        status: err instanceof ApiError ? err.status : 0,
        text: err instanceof Error ? err.message : String(err),
      });
    }
  }, []);

  useEffect(() => {
    void read();
  }, [read]);

  if (why) {
    return (
      <p data-shell-sessions-why={String(why.status)} className="text-muted-foreground text-xs">
        {why.status === 503
          ? `this host cannot hold a shared session: ${why.text}`
          : `could not read the sessions: ${why.text}`}
      </p>
    );
  }
  if (sessions === null) {
    return (
      <p data-shell-sessions-reading="" className="text-muted-foreground text-xs">
        reading the sessions…
      </p>
    );
  }

  const mine = project ? `projectile/${project.replace(/[.: ]/g, "_")}` : "";

  return (
    <div className="flex flex-col gap-2" data-shell-sessions={String(sessions.length)}>
      {sessions.length === 0 ? (
        // NOTHING RUNNING, said as itself: the host answered and held none.
        <p data-shell-sessions-empty="" className="text-muted-foreground text-xs">
          no sessions on this host yet. Opening a shell makes one.
        </p>
      ) : (
        <ul className="flex flex-col gap-1">
          {sessions.map((s) => (
            <li
              key={s.name}
              data-shell-session={s.name}
              data-shell-session-mine={s.name === mine ? "yes" : "no"}
              className="flex flex-col gap-0.5 border-border-soft border-b py-1.5"
            >
              <div className="flex flex-wrap items-baseline gap-2">
                <span className="font-mono text-xs">{s.name}</span>
                <span className="text-muted-foreground text-xs">
                  {s.windows.length} window{s.windows.length === 1 ? "" : "s"}
                </span>
                {/*
                  ATTACHED IS A COUNT, NOT A FLAG. Several clients on one session
                  is the normal case here - the panel, their editor and an ssh -
                  and "attached" alone would hide that.
                */}
                {s.attached > 0 ? (
                  <span data-shell-session-attached={String(s.attached)} className="text-xs">
                    {s.attached} attached
                  </span>
                ) : (
                  <span className="text-muted-foreground text-xs">nobody attached</span>
                )}
              </div>
              {/*
                THE LINE TO TYPE ELSEWHERE. This is the answer to the question
                the operator actually asked - reaching it over ssh - so it is on
                screen rather than in documentation.
              */}
              <code className="text-[10px] text-muted-foreground">byobu attach -t {s.name}</code>
              {s.windows.length > 0 ? (
                <div className="flex flex-wrap gap-1">
                  {s.windows.map((w) => (
                    <span
                      key={w.index}
                      data-shell-window={`${s.name}:${w.index}`}
                      className={
                        w.active
                          ? "rounded border border-primary px-1.5 py-0.5 font-mono text-[10px] text-primary"
                          : "rounded border border-border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                      }
                    >
                      {w.index}:{w.name}
                      {/* What is actually running, which is the thing a window
                          name often does not say. */}
                      {w.command && w.command !== w.name ? ` (${w.command})` : ""}
                    </span>
                  ))}
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export default ShellSessions;
