import {
  Activity as ActivityIcon,
  Boxes,
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
  X,
} from "lucide-react";
import type { ReactNode } from "react";
import { NavLink, useNavigate } from "react-router-dom";

import { FreshBanner } from "@/components/FreshBanner";
import { TokenBar } from "@/components/TokenBar";
import { api } from "@/lib/api";
import { useRoomList, useUnread } from "@/lib/unread";
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
      // THE EXACT COUNT, beside the one a person reads. The text is capped at
      // "99+" so a badge cannot grow the row, and anything summing these from
      // the text would quietly lose the difference the moment a room passed a
      // hundred - which on this node is a Tuesday. The attribute is what the
      // rail's own totals are checked against.
      data-unread-count={n}
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
  const { shown: rooms, hidden, close, reopen } = useRoomList();
  // THE TWO TOTALS, summed from the same per-room counts the dots are drawn
  // from - so a reader who sees the heading move can find the room it moved
  // for, and the three numbers can never disagree with each other.
  //
  // Summed over `counts` itself rather than over the room lists, because the
  // node answers with what it has unread and the sidebar's two lists are a
  // VIEW of that: a room the reader closed still receives, and a room the list
  // has not caught up with yet would silently drop out of the total.
  const allUnread = Object.values(counts).reduce((sum, n) => sum + n, 0);
  const hiddenUnread = hidden.reduce((sum, room) => sum + (counts[room] ?? 0), 0);
  // For the create button below: a new room is somewhere to go, not just a row
  // in the list, so it navigates rather than leaving you where you were.
  const navigate = useNavigate();
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
          {/*
            WHICH PROJECT THIS TOKEN WRITES IN. The rail says "flowy" at the
            top and that is the product's name, not the project's - and every
            project has a #general, so a room name stops being an address the
            moment there is more than one. Two messages went into pa/#general
            from this machine and nobody saw them for ten minutes.
          */}
          <NavLink to="/projects" className={navClass}>
            <Boxes className="h-4 w-4" />
            projects
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

        {/*
          THE ROOMS SCROLL, NOT THE PAGE.

          The aside had no height bound and no overflow, so a node with more
          rooms than fit simply made the sidebar taller than the viewport and
          the whole page scrolled - reported by the operator at 26 rooms, with
          the token bar pushed off the bottom.

          min-h-0 is the half that is easy to leave out and does nothing
          without: a flex child's default min-height is its content, so
          overflow-y-auto on its own never gets a box smaller than the list and
          never scrolls. flex-1 gives it the leftover height, min-h-0 lets it be
          smaller than its contents, and only then does the overflow do
          anything.

          It sits on the rooms block rather than the aside so the nav above and
          the token bar below stay where they are - a sidebar that scrolls as
          one takes the way out with it.
        */}
        {/*
          THE HEADER IS OUTSIDE THE SCROLLING LIST, and the create button with it.
          Inside, it scrolled away with the rooms - and the browser check found it
          the hard way: the button rendered, was visible and enabled, and a click
          landed on the aside because the point was clipped by the scroll
          container. A control you cannot reach is the same as no control, which
          is the whole defect this row is closing.
        */}
        <div className="flex shrink-0 items-center justify-between px-2 pb-1">
          <span className="font-medium text-muted-foreground text-xs uppercase tracking-wide">
            rooms
          </span>
          {/*
            THE GLOBAL UNREAD, and the operator placed it here: "we can have
            global unread counter at the right of ROOMS".

            EVERY room, closed ones included - their ruling, "global is global".
            That keeps closing purely visual, which is what closing already
            means: a fact about what a reader looks at and never about what the
            node delivers. It also means this number can ship without waiting on
            an ignore verb.

            Absent at zero rather than a "0". A badge that clears is a badge
            that is gone from the document, which is UnreadDot's rule above and
            is what makes the element itself the assertion.
          */}
          {allUnread > 0 ? (
            <span
              data-rooms-unread={allUnread}
              title={`${allUnread} unread across every room, closed ones included`}
              className="ml-auto mr-1 rounded-full bg-primary px-1.5 font-mono text-[10px] text-primary-foreground tabular-nums"
            >
              {allUnread}
            </span>
          ) : null}
          {/*
              CREATING A ROOM IS A BUTTON NOW. POST /api/rooms has existed since
              rooms became objects and nothing in this console called it, so the
              operator could read rooms and not make one - which reads as a
              missing feature and is a missing reader. Of the four room doors
              this console called exactly one.

              window.prompt rather than a dialog, deliberately: the name is the
              whole input, a taken one is answered by the node, and a modal for
              one field is the kind of thing that does not get built and so the
              button does not either.
            */}
          <button
            type="button"
            aria-label="create a room"
            title="create a room"
            data-room-create=""
            className="rounded px-1 text-muted-foreground hover:bg-accent hover:text-foreground"
            onClick={async () => {
              const name = window.prompt("room name")?.trim();
              if (!name) return;
              try {
                const made = await api.createRoom(name);
                navigate(`/chat/${made.room.name}`);
              } catch (err) {
                // The node's own sentence, which already tells "that name is
                // taken" from "that is not a name" - see api_rooms.go. A
                // generic "could not create" would throw that away.
                window.alert(err instanceof Error ? err.message : "the node refused");
              }
            }}
          >
            +
          </button>
        </div>
        {/*
          NAMED, because "is this room in the sidebar" has to be asked of the
          SIDEBAR. Every message in the overview's inbox card carries a link to
          the room it was said in, so `a[href="/chat/general"]` matches dozens of
          things on a busy node - and a check that asked the document rather than
          this list could never see a room leave it.
        */}
        <div data-room-list="" className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto">
          {rooms.map((room) => (
            <div key={room} className="group relative flex items-center">
              {/* navClass is a FUNCTION, because NavLink hands it isActive.
                  cn(navClass, ...) silently dropped it - clsx ignores functions
                  - so this link rendered with "flex-1 pr-7" and nothing else:
                  no text-sm, so 16px instead of 14px, and no flex, so the hash
                  and the room name stacked one above the other. The operator
                  sent a screenshot of it. It is called with isActive here so
                  the value, not the function, reaches cn. */}
              <NavLink
                to={`/chat/${room}`}
                className={({ isActive }) => cn(navClass({ isActive }), "flex-1 pr-7")}
              >
                <Hash className="h-4 w-4" />
                {room}
                <UnreadDot room={room} n={counts[room] ?? 0} />
              </NavLink>
              {/*
                CLOSE, and it is deliberately not "leave". Leaving is a
                permission act - it empties your role in the room - and the
                operator did it, got "you are not a member", and watched the
                sidebar not change, because the list is every room in the
                project. Closing is a fact about this READER: which rooms they
                want in front of them. The room stays readable and stays theirs
                to reopen, which is why this needs no confirmation.

                Shown on hover so a list of thirty rooms is not a list of thirty
                buttons, and titled rather than labelled for the same reason.
              */}
              <button
                type="button"
                data-close-room={room}
                title={`close ${room} - it stays readable, and reopens from the line below`}
                aria-label={`close ${room}`}
                // VISIBLE ON EVERY ROW, faint, and solid when you point at it.
                //
                // It was `hidden group-hover:block`, which is the ordinary
                // pattern and was wrong here: the operator asked how to remove
                // a room from the list THREE TIMES, twice after this shipped.
                // From where they were sitting the sidebar offered nothing -
                // the control only existed once the pointer happened to be on
                // the right row. A control you find by accident is not a
                // control, and "ask the person who built it" is not an
                // affordance.
                //
                // opacity rather than display, so the row does not reflow under
                // the pointer and the target does not move as you approach it.
                className="absolute right-1 rounded p-1 text-muted-foreground opacity-40 hover:bg-muted hover:text-foreground hover:opacity-100"
                onClick={() => void close(room)}
              >
                <X className="h-3 w-3" />
              </button>
            </div>
          ))}
          {/*
            WHAT WAS CLOSED IS NOT GONE, and the sidebar says so rather than
            leaving a reader to wonder where a room went. A closed room with no
            way back is a room somebody has to be told about; this is the way
            back and it is one click.
          */}
          {hidden.length > 0 ? (
            <details className="mt-1 px-2 text-muted-foreground text-xs" data-closed-rooms="">
              {/*
                AND THE CLOSED PILE CARRIES ITS OWN, the operator's second
                ruling: "closed accordeon tiitlle can have closed counter".

                It is what makes the global number readable. A total that never
                reaches zero says nothing on its own; beside this one a reader
                can see WHICH HALF the noise is in and decide whether to go and
                look. Drawn as absence rather than as a room, because a pile of
                closed rooms is not a place - it is what is not in front of you.
              */}
              <summary className="cursor-pointer py-1">
                {hidden.length} closed
                {hiddenUnread > 0 ? (
                  <span
                    data-closed-unread={hiddenUnread}
                    title={`${hiddenUnread} unread in rooms you have closed`}
                    className="ml-1 font-mono tabular-nums text-foreground"
                  >
                    · {hiddenUnread} unread
                  </span>
                ) : null}
              </summary>
              {hidden.map((room) => (
                <button
                  key={room}
                  type="button"
                  data-reopen-room={room}
                  className="flex w-full items-center gap-1 rounded px-1 py-0.5 text-left hover:bg-muted hover:text-foreground"
                  onClick={() => void reopen(room)}
                >
                  <Hash className="h-3 w-3" />
                  {room}
                </button>
              ))}
            </details>
          ) : null}
        </div>

        {/* shrink-0 so the rooms list cannot squeeze it away; mt-auto is gone
            because the rooms block now takes the slack that used to push this
            down. */}
        <div className="shrink-0">
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
