import {
  Activity as ActivityIcon,
  FileText,
  GitBranch,
  Hash,
  History,
  Home as HomeIcon,
  Inbox as InboxIcon,
  ListChecks,
  ListTree,
  Lock,
} from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { NavLink } from "react-router-dom";

import { FreshBanner } from "@/components/FreshBanner";
import { TokenBar } from "@/components/TokenBar";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";

/** rooms the sidebar offers by name. Any other room is reachable by URL. */
const ROOMS = ["general", "handoffs", "incidents"];

function navClass({ isActive }: { isActive: boolean }) {
  return cn(
    "flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors",
    isActive ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-accent/60",
  );
}

/**
 * Unread is what the inbox holds per room: messages this token may read and did
 * not write, said since the node's reader mark moved for it. It is the same
 * permission filter every read carries, and it is not a per-tab latch - opening
 * a room does not clear the badge, the inbox does, because the waiter's mark is
 * the one position the log keeps for this principal.
 */
function useUnreadByRoom(): Record<string, number> {
  const { token } = useSession();
  const [counts, setCounts] = useState<Record<string, number>>({});
  useEffect(() => {
    if (!token) {
      setCounts({});
      return;
    }
    let stopped = false;
    const load = () => {
      api
        .inbox()
        .then((page) => {
          if (stopped) return;
          const next: Record<string, number> = {};
          for (const e of page.events) next[e.room] = (next[e.room] ?? 0) + 1;
          setCounts(next);
        })
        .catch(() => {});
    };
    load();
    const every = setInterval(load, 20_000);
    return () => {
      stopped = true;
      clearInterval(every);
    };
  }, [token]);
  return counts;
}

/** UnreadDot is the badge itself: there when something waits, absent when not. */
function UnreadDot({ n }: { n: number }) {
  if (n <= 0) return null;
  return (
    <span className="ml-auto inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 font-mono text-[10px] text-primary-foreground">
      {n > 99 ? "99+" : n}
    </span>
  );
}

/** Shell is the frame every route renders inside: navigation, and the token. */
export function Shell({ children }: { children: ReactNode }) {
  const unread = useUnreadByRoom();
  return (
    <div className="flex h-full">
      <aside className="flex w-60 shrink-0 flex-col gap-4 border-border border-r bg-card/40 p-3">
        <div className="px-2 pt-1">
          <div className="font-semibold text-lg tracking-tight">flowy</div>
          <div className="text-muted-foreground text-xs">handoff fabric console</div>
        </div>

        <nav className="flex flex-col gap-0.5">
          <NavLink to="/" className={navClass} end>
            <HomeIcon className="h-4 w-4" />
            overview
          </NavLink>
          <NavLink to="/inbox" className={navClass}>
            <InboxIcon className="h-4 w-4" />
            inbox
          </NavLink>
          {/*
            Above the rooms and not among them, because it is not one. The
            padlock is the point of the row: it is the only place in this
            console where what you write is read by one named person, and it
            has to be told apart from a room at a glance.
          */}
          <NavLink to="/direct" className={navClass}>
            <Lock className="h-4 w-4" />
            direct
          </NavLink>
          {/*
            One row, not one per project: the page is the queue across every
            project this token reads, and it says so on itself.
          */}
          <NavLink to="/todos" className={navClass}>
            <ListChecks className="h-4 w-4" />
            todos
          </NavLink>
          <NavLink to="/reports" className={navClass}>
            <FileText className="h-4 w-4" />
            reports
          </NavLink>
          <NavLink to="/worklog" className={navClass}>
            <History className="h-4 w-4" />
            worklog
          </NavLink>
          <NavLink to="/activity" className={navClass}>
            <ListTree className="h-4 w-4" />
            activity
          </NavLink>
          <NavLink to="/metrics" className={navClass}>
            <ActivityIcon className="h-4 w-4" />
            metrics
          </NavLink>
          <NavLink to="/traces" className={navClass}>
            <GitBranch className="h-4 w-4" />
            traces
          </NavLink>
        </nav>

        <div className="flex flex-col gap-0.5">
          <div className="px-2 pb-1 font-medium text-muted-foreground text-xs uppercase tracking-wide">
            rooms
          </div>
          {ROOMS.map((room) => (
            <NavLink key={room} to={`/chat/${room}`} className={navClass}>
              <Hash className="h-4 w-4" />
              {room}
              <UnreadDot n={unread[room] ?? 0} />
            </NavLink>
          ))}
        </div>

        <div className="mt-auto">
          <TokenBar />
        </div>
      </aside>

      <main className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {/*
          Above every route rather than inside one: a tab running a replaced
          console is a fact about the tab, not about whichever page it happens
          to be showing.
        */}
        <FreshBanner />
        <div className="min-h-0 flex-1 overflow-hidden">{children}</div>
      </main>
    </div>
  );
}
