import {
  Activity as ActivityIcon,
  GitBranch,
  Hash,
  Home as HomeIcon,
  Inbox as InboxIcon,
  ListTree,
} from "lucide-react";
import type { ReactNode } from "react";
import { NavLink } from "react-router-dom";

import { TokenBar } from "@/components/TokenBar";
import { cn } from "@/lib/utils";

/** rooms the sidebar offers by name. Any other room is reachable by URL. */
const ROOMS = ["general", "handoffs", "incidents"];

function navClass({ isActive }: { isActive: boolean }) {
  return cn(
    "flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors",
    isActive ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-accent/60",
  );
}

/** Shell is the frame every route renders inside: navigation, and the token. */
export function Shell({ children }: { children: ReactNode }) {
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
            </NavLink>
          ))}
        </div>

        <div className="mt-auto">
          <TokenBar />
        </div>
      </aside>

      <main className="min-w-0 flex-1 overflow-hidden">{children}</main>
    </div>
  );
}
