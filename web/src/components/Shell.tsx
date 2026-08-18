import {
  Activity as ActivityIcon,
  Brain,
  Bug,
  FilePlus,
  FileText,
  GitBranch,
  Hash,
  History,
  Home as HomeIcon,
  Inbox as InboxIcon,
  ListChecks,
  ListTree,
  Lock,
  Shapes,
  UserRound,
} from "lucide-react";
import type { ReactNode } from "react";
import { NavLink } from "react-router-dom";

import { FreshBanner } from "@/components/FreshBanner";
import { TokenBar } from "@/components/TokenBar";
import { useRooms, useUnread } from "@/lib/unread";
import { cn } from "@/lib/utils";

function navClass({ isActive }: { isActive: boolean }) {
  return cn(
    "flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors",
    isActive ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-accent/60",
  );
}

/**
 * UnreadDot is the badge itself: there when something waits, absent when not.
 *
 * Absent rather than a zero, which is what makes the element the assertion: a
 * badge that clears is a badge that is gone from the document. The room is on
 * it so a check can find the one it means - see scripts/unread-check.mjs.
 */
function UnreadDot({ room, n }: { room: string; n: number }) {
  if (n <= 0) return null;
  return (
    <span
      data-unread={room}
      className="ml-auto inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 font-mono text-[10px] text-primary-foreground"
    >
      {n > 99 ? "99+" : n}
    </span>
  );
}

/** Shell is the frame every route renders inside: navigation, and the token. */
export function Shell({ children }: { children: ReactNode }) {
  // What the node's reader marks say is unread, per room. The counting and the
  // clearing both live in lib/unread - the sidebar draws it and does not own
  // it, because the room view is what knows when something has been read.
  const { counts } = useUnread();
  // The node's rooms, not this file's idea of them. See useRooms.
  const rooms = useRooms();
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
          {/*
            WRITING IS A PLACE, not a button hidden on a list page. Every list
            in this console is read-only by design - the store owns the scope
            rules and a second write door on each page would be several - so
            the one door gets a name in the rail.
          */}
          <NavLink to="/new" className={navClass}>
            <FilePlus className="h-4 w-4" />
            new
          </NavLink>
          <NavLink to="/todos" className={navClass}>
            <ListChecks className="h-4 w-4" />
            todos
          </NavLink>
          <NavLink to="/memory" className={navClass}>
            <Brain className="h-4 w-4" />
            memory
          </NavLink>
          <NavLink to="/reports" className={navClass}>
            <FileText className="h-4 w-4" />
            reports
          </NavLink>
          <NavLink to="/findings" className={navClass}>
            <Bug className="h-4 w-4" />
            findings
          </NavLink>
          <NavLink to="/diagrams" className={navClass}>
            <Shapes className="h-4 w-4" />
            diagrams
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
          <NavLink to="/profile" className={navClass}>
            <UserRound className="h-4 w-4" />
            profile
          </NavLink>
        </nav>

        <div className="flex flex-col gap-0.5">
          <div className="px-2 pb-1 font-medium text-muted-foreground text-xs uppercase tracking-wide">
            rooms
          </div>
          {rooms.map((room) => (
            <NavLink key={room} to={`/chat/${room}`} className={navClass}>
              <Hash className="h-4 w-4" />
              {room}
              <UnreadDot room={room} n={counts[room] ?? 0} />
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
