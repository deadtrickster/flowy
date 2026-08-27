import {
  Activity as ActivityIcon,
  Bell,
  BellOff,
  BookOpen,
  Bookmark,
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
  Layers,
  LayoutDashboard,
  ListChecks,
  ListTree,
  Lock,
  Menu,
  Shapes,
  UserRound,
  X,
} from "lucide-react";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { NavLink, useLocation, useNavigate } from "react-router-dom";

import { CredentialBanner } from "@/components/CredentialBanner";
import { FreshBanner } from "@/components/FreshBanner";
import { ResizeHandle } from "@/components/ResizeHandle";
import { TokenBar } from "@/components/TokenBar";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { useIgnoredRooms, useRoomList, useUnread } from "@/lib/unread";
import { cn } from "@/lib/utils";
import { useWaiting } from "@/lib/waiting";

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

/**
 * WHICH PROJECT AM I IN, in the one place every page shows.
 *
 * 01M10V97MD, the operator: "No currnet project name anywhere ... no way to
 * quickly switch between projects - replacy 'handoff fabric console' with a
 * project dropdown". The subtitle it replaces was a slogan, and a slogan is
 * what this corner said while two messages went into pa's #general instead of
 * flowy's - routes/Projects.tsx records that one. Every project has a #general,
 * so a room name is not an address once there is more than one project.
 *
 * SWITCHING IS A SESSION ACT and only a person has one: entering needs a cookie
 * and a membership, and an agent credential has neither. So a seat sees the
 * name and no control, which is the honest rendering - not a dropdown that
 * refuses when pressed.
 *
 * ABSENT IS NOT EMPTY, the same distinction the field carries on the node.
 * memberships null means nobody said (an agent, or a whoami still in flight) and
 * draws no control; [] means this person belongs to nothing, and that gets a
 * word rather than an empty menu, because a menu with no items reads as broken.
 */
function ProjectBadge() {
  const { whoami, refresh } = useSession();
  const [busy, setBusy] = useState("");
  const [refused, setRefused] = useState("");

  const here = whoami?.project ?? "";
  const mine = whoami?.memberships ?? null;
  // The list is what a person may ENTER, and being here already is not a
  // reason to leave the current project out of it - it is the one that has to
  // be shown as current.
  const others = (mine ?? []).filter((p) => p !== here);

  const enter = async (project: string) => {
    setBusy(project);
    setRefused("");
    try {
      // The node answers where the writes now land rather than "ok", so the
      // refusal is its sentence and not a guess made here.
      await api.enterProject(project);
      refresh();
    } catch (err) {
      setRefused(String(err));
    } finally {
      setBusy("");
    }
  };

  if (!whoami) {
    return <div className="text-muted-foreground text-xs">handoff fabric console</div>;
  }
  return (
    <div className="flex flex-col gap-0.5">
      <div className="flex items-center gap-1.5">
        <span
          data-current-project={here || "none"}
          className="font-mono text-foreground text-xs"
          title={
            whoami.project_fixture
              ? `you are writing into ${here}, which is demo seed data`
              : `you are writing into ${here || "no project"}`
          }
        >
          {here || "no project"}
        </span>
        {/* A FIXTURE IS WRITABLE, which is exactly why it has to be said. */}
        {whoami.project_fixture ? (
          <span className="rounded bg-destructive/15 px-1 text-[10px] text-destructive">
            fixture
          </span>
        ) : null}
      </div>
      {mine === null ? null : others.length === 0 ? (
        <span className="text-[10px] text-muted-foreground">
          {mine.length === 0 ? "no other project to switch to" : "the only one you are in"}
        </span>
      ) : (
        <div className="flex flex-wrap items-center gap-1" data-project-switcher={others.length}>
          {others.map((p) => (
            <button
              key={p}
              type="button"
              data-enter-project={p}
              disabled={busy !== ""}
              onClick={() => void enter(p)}
              className="cursor-pointer rounded border border-border px-1 font-mono text-[10px] text-muted-foreground transition hover:border-primary/50 hover:text-foreground disabled:opacity-50"
            >
              {busy === p ? `${p}…` : p}
            </button>
          ))}
        </div>
      )}
      {refused ? (
        <span data-enter-refused className="text-[10px] text-destructive">
          {refused}
        </span>
      ) : null}
    </div>
  );
}

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

/**
 * WaitingDot is the same badge for a rail row that is not a room: how much work
 * is waiting for this principal behind that link.
 *
 * ABSENT ON NULL AND ABSENT ON ZERO, and those are two different reasons that
 * happen to look the same. Zero is "nothing is waiting", null is "we could not
 * ask" - see lib/waiting for why the distinction is kept rather than collapsed.
 * Neither draws a badge, because a badge means WORK IS HERE and a node that
 * cannot be read is not work. What must never happen is the third thing: a
 * failed read rendering as a confident "0".
 *
 * Named by its row rather than by its endpoint, because a check asks the
 * SIDEBAR which row carries a number - see scripts/rail-act-check.mjs.
 */
function WaitingDot({ row, n }: { row: string; n: number | null }) {
  if (n === null || n <= 0) return null;
  return (
    <span
      data-waiting={row}
      data-waiting-count={n}
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
  const { counts, direct } = useUnread();
  // The node's rooms, not this file's idea of them. See useRooms.
  const { shown: rooms, hidden, close, reopen } = useRoomList();
  const { isIgnored, ignore, unignore } = useIgnoredRooms();
  // How much is waiting for this principal, for the two rows where "waiting"
  // has an answer that ever reaches zero. lib/waiting holds the reasoning for
  // which rows those are and why the other eleven carry nothing.
  const waiting = useWaiting();
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
  // THE NAV IS A DRAWER ON A SMALL SCREEN AND A COLUMN ON A LARGE ONE.
  //
  // Measured in a real browser against the deployed node, 2026-08-20, at the
  // size the operator was actually holding:
  //
  //   390x664   aside 240  main 150  composer 26px wide  rooms on screen 0/28
  //   768x1024  aside 240  main 528                      rooms 10/28
  //   1600x1000 aside 240  main 1360 composer 920
  //
  // The column was a hard w-60 with no breakpoint anywhere in this file, so on
  // a phone it took 62% of the width and its thirteen links pushed the ROOMS
  // heading below the fold - the operator's words were "not enough vertical
  // space, simply not visible", and the measurement agrees: not one of
  // twenty-eight rooms was reachable. Nothing signalled it either, because the
  // page does not overflow horizontally; it simply looked broken.
  //
  // Open state rather than a CSS-only :target or a checkbox hack, because the
  // drawer has to close on navigation - a menu that stays over the page you
  // just asked for is the second half of this same complaint.
  const [navOpen, setNavOpen] = useState(false);
  // THE NAV'S WIDTH, when it is a column. Null until something says otherwise -
  // the Tailwind class stays the default, so a reader who has never dragged it
  // gets exactly what they got before and nothing is stored on their behalf.
  //
  // Not applied below the breakpoint: down there the nav is a drawer over the
  // page, and a width dragged on a desk has no meaning on a phone.
  const [navWidth, setNavWidth] = useState<number | null>(null);
  // CLOSING IS TIED TO ARRIVING SOMEWHERE, not to the tap that started it. A
  // click handler on the drawer closes it when somebody taps dead space in it,
  // and does not close it when they reach a page by keyboard - both wrong, and
  // the second is the one nobody tests. The path changing is the actual event:
  // you asked for a page, so the menu is done.
  const { pathname } = useLocation();
  const shownFor = useRef(pathname);
  useEffect(() => {
    if (shownFor.current === pathname) return;
    shownFor.current = pathname;
    setNavOpen(false);
  }, [pathname]);
  return (
    <div className="flex h-full">
      {/*
        THE BUTTON THAT OPENS IT, and it only exists where the drawer does.
        md:hidden rather than a conditional render: the breakpoint is the one
        fact deciding both halves, and two ways of asking it drift.
      */}
      <button
        type="button"
        data-nav-open=""
        aria-label="rooms and navigation"
        aria-expanded={navOpen}
        className="fixed top-2 left-2 z-50 rounded-md border border-border bg-card p-2 shadow-sm md:hidden"
        onClick={() => setNavOpen((open) => !open)}
      >
        {navOpen ? <X className="h-5 w-5" /> : <Menu className="h-5 w-5" />}
      </button>
      {/*
        The backdrop is what makes it a drawer rather than a panel stuck open:
        one tap anywhere on the page you were reading puts you back on it.
      */}
      {navOpen ? (
        <button
          type="button"
          aria-label="close navigation"
          data-nav-backdrop=""
          className="fixed inset-0 z-30 bg-background/70 md:hidden"
          onClick={() => setNavOpen(false)}
        />
      ) : null}
      <aside
        style={
          navWidth !== null ? ({ "--nav-w": `${navWidth}px` } as React.CSSProperties) : undefined
        }
        data-nav=""
        data-nav-state={navOpen ? "open" : "closed"}
        className={cn(
          // WIDER AS A DRAWER than as a column, because a room called
          // doc-01M07SCJ5XDXKCSY4SJ1NR87 does not fit in 240px and this
          // console names rooms after ULIDs. 18rem still leaves the page
          // visible behind the backdrop, which is what says the drawer is over
          // something rather than being the whole app.
          // ONE width utility at md, not two. The first cut kept md:w-60 and added
          // md:w-[var(--nav-w)] beside it, and the drag did nothing: two width
          // utilities at the same breakpoint are decided by the order Tailwind
          // emits them, not by the order they appear in the attribute. The var
          // carries its own default instead, so there is one rule and the
          // fallback IS the old value.
          "flex w-72 shrink-0 flex-col gap-4 border-border border-r p-3 md:w-[var(--nav-w,15rem)]",
          // OPAQUE AS A DRAWER, translucent as a column. bg-card/40 over a
          // transcript is unreadable - both layers of text land on top of each
          // other - and the first build of this drawer shipped exactly that.
          "bg-card md:bg-card/40",
          // ONE SCROLL REGION ON A SHORT SCREEN. Thirteen nav links are taller
          // than a 664px phone on their own, so with the rooms in their own
          // scroller the rooms got nothing: measured 0 of 28 reachable with the
          // drawer OPEN, which is the operator's complaint surviving the fix
          // that was supposed to answer it. Above the breakpoint the column
          // keeps its old shape, where the nav is fixed, the rooms scroll and
          // the token bar stays put - until the fixed parts stop fitting. The
          // nav keeps its height when there is room (md:min-h-0 below) and
          // becomes a scroller itself when a live token bar leaves it less.
          "overflow-y-auto md:overflow-y-visible",
          // Off-canvas below the breakpoint, an ordinary column at and above it.
          // `fixed` takes it out of the flex row entirely, which is the point:
          // a hidden-but-laid-out column still costs its 240px.
          "fixed inset-y-0 left-0 z-40 transition-transform md:static md:z-auto md:translate-x-0",
          navOpen ? "translate-x-0 shadow-xl" : "-translate-x-full",
          // Room for the open/close button, which sits over this column's own
          // header while the drawer is open.
          "pt-12 md:pt-3",
        )}
      >
        <div className="px-2 pt-1">
          <div className="font-semibold text-lg tracking-tight">flowy</div>
          <ProjectBadge />
        </div>

        {/*
          ROOMS FIRST ON A PHONE, and this is a judgement rather than a
          measurement, so it is written down. With the drawer scrolling, all 28
          rooms are reachable - but only after scrolling past thirteen nav
          links, and measured with the drawer OPEN not one room was on screen.
          On a phone this console is a chat client: the rooms are the thing, and
          overview/metrics/traces are where you go afterwards. Above the
          breakpoint the source order stands, because a mouse and a tall window
          make the whole column visible at once and moving things would only
          surprise somebody who already knows where they are.

          CSS order rather than two renderings of the same list, so there is one
          nav in the document and a check cannot pass against the copy that is
          not showing.
        */}
        <nav className="order-3 flex flex-col gap-0.5 md:order-none md:min-h-0 md:overflow-y-auto">
          <NavLink to="/" className={navClass} end>
            <HomeIcon className="h-4 w-4" />
            overview
          </NavLink>
          {/*
            THE TASK INBOX, not the message one, and the badge counts what the
            page lists. /inbox reads /api/inbox/tasks - work handed to this
            principal by name - and the rail was the last place that fact was
            invisible: an agent could be holding four delegated tasks and the
            only way to find out was to click.
          */}
          <NavLink to="/inbox" className={navClass}>
            <InboxIcon className="h-4 w-4" />
            inbox
            <WaitingDot row="inbox" n={waiting.tasks} />
          </NavLink>
          {/*
            Above the rooms and not among them, because it is not one. The
            padlock is the point of the row: it is the only place in this
            console where what you write is read by one named person, and it
            has to be told apart from a room at a glance.

            AND IT CARRIES A NUMBER NOW, which it could not before: /api/dm
            takes a raw cursor that lived in the tab, so nothing on the node
            said which private messages this person had read and the only
            available number was "how many DMs exist" - a badge that never
            clears, which is worse than none, because it teaches a reader to
            ignore the two above it that do. 01M0GP1S0K put the mark where the
            rooms already had theirs: a console reader over the private log,
            moved forward when the page reports having reached a message.

            Null and zero draw the same nothing here for two different reasons -
            see WaitingDot, and lib/unread for which is which.
          */}
          <NavLink to="/direct" className={navClass}>
            <Lock className="h-4 w-4" />
            direct
            <WaitingDot row="direct" n={direct} />
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
          {/*
            OPEN ROWS ASSIGNED TO THIS PRINCIPAL - mine_todo from /api/nag, so
            the rail and the board nag report the same number. They were two
            answers to one question before this, and only one of them was ever
            on screen.
          */}
          {/* A reader's own pile, beside the queue rather than inside a room:
              what somebody kept is theirs and spans every room they read. See
              routes/Bookmarks. */}
          <NavLink to="/bookmarks" className={navClass}>
            <Bookmark className="h-4 w-4" />
            kept
          </NavLink>
          <NavLink to="/todos" className={navClass}>
            <ListChecks className="h-4 w-4" />
            todos
            <WaitingDot row="todos" n={waiting.todos} />
          </NavLink>
          {/*
            WHICH PROJECT THIS TOKEN WRITES IN. The rail says "flowy" at the
            top and that is the product's name, not the project's - and every
            project has a #general, so a room name stops being an address the
            moment there is more than one. Two messages went into pa/#general
            from this machine and nobody saw them for ten minutes.
          */}
          {/*
            FROM HERE DOWN, NO NUMBERS, and that is the answer to 01M0GGEW74
            rather than a corner left uncut. The row read "the rail carries one
            number and thirteen rows carry none"; thirteen badges is the wrong
            fix. What these rows could count is HOW MANY EXIST - 47 memories,
            9 reports - which on a working node is never zero and never goes
            down. A badge that never clears is decoration, and decoration next
            to a badge that does clear is what stops anybody reading either.

            The question these rows deserve is "did it move since I looked",
            which is a read mark per list, which the node does not keep. Same
            gap as direct above. Until it does, silence here is the honest
            answer and the two badges above keep their meaning.
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
          <NavLink to="/openspec" className={navClass}>
            <Layers className="h-4 w-4" />
            openspec
          </NavLink>
          <NavLink to="/findings" className={navClass}>
            <Bug className="h-4 w-4" />
            findings
          </NavLink>
          <NavLink to="/diagrams" className={navClass}>
            <Shapes className="h-4 w-4" />
            diagrams
          </NavLink>
          <NavLink to="/dashboards" className={navClass}>
            <LayoutDashboard className="h-4 w-4" />
            dashboards
          </NavLink>
          <NavLink to="/skills" className={navClass}>
            <BookOpen className="h-4 w-4" />
            skills
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

          min-h-28 is the floor that keeps the rooms reachable: a flex child's
          default min-height is its content, so overflow-y-auto on its own never
          gets a box smaller than the list and never scrolls. flex-1 gives it
          the leftover height, and min-h-28 - not min-h-0 - lets it shrink
          under its contents but never to nothing. With no floor at all, a live
          token bar taller than the spare room squeezes the list to height 0:
          its rows render clipped, clicks on them land on the token bar, and
          the fixed parts overflow the page. The floor is the part that says
          the rooms win the last rows; the nav scrolls instead (its own
          md:min-h-0 below).

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
        <div className="order-1 flex shrink-0 items-center justify-between px-2 pb-1 md:order-none">
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
        <div
          data-room-list=""
          className="order-2 flex flex-col gap-0.5 md:order-none md:min-h-28 md:flex-1 md:overflow-y-auto"
        >
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
                // pr-14 AND NOT pr-7. The room name has to clear BOTH controls now -
                // close at right-1 and ignore at right-7 - and a padding sized
                // for one of them lets a long name run underneath the other.
                // The previous value was correct for one button and became a
                // lie the moment a second arrived beside it.
                className={({ isActive }) => cn(navClass({ isActive }), "flex-1 pr-14")}
              >
                <Hash className="h-4 w-4" />
                {room}
                {/* AN IGNORED ROOM DRAWS NO BADGE, and the node agrees rather
                    than only the console: /api/inbox/unread answers 0 for a
                    room on this list, so the two cannot disagree while a
                    reader is looking at them. Suppressed here as well because
                    the count in hand was read before the click and a reader
                    should not watch a badge they just silenced sit there until
                    the next poll. */}
                <UnreadDot room={room} n={isIgnored(room) ? 0 : (counts[room] ?? 0)} />
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
              {/*
                IGNORE, WHICH IS NOT CLOSE AND NOT LEAVE. 01M0GHF3JQ, the
                operator: "humans close windows to focus but dont want to miss.
                what would be a 'real close' is *ignoring*".

                  close    not in front of me       sidebar only
                  ignore   do not tell me about it  no badge, no wake, still
                                                    in the list and still
                                                    readable
                  leave    I am not a member        a permission act

                Beside close rather than replacing it, because the two are
                different axes and a reader wants both: a room can be closed
                and still shout, or sit in the sidebar and stay quiet. One
                control for two states is the collapse this row is about.

                It draws the state it is IN rather than the action it performs
                - a struck bell means "you are not being told" - because the
                row already reads as a list of rooms rather than a list of
                verbs, and a reader scanning for the quiet ones is scanning for
                a state.
              */}
              <button
                type="button"
                data-ignore-room={room}
                data-ignored={isIgnored(room) ? "" : undefined}
                title={
                  isIgnored(room)
                    ? `${room} is ignored - no badge and nothing wakes you. Click to hear it again`
                    : `ignore ${room} - it stays in the list and stays readable, and stops telling you`
                }
                aria-label={isIgnored(room) ? `stop ignoring ${room}` : `ignore ${room}`}
                className={cn(
                  "absolute right-7 rounded p-1 hover:bg-muted hover:text-foreground hover:opacity-100",
                  isIgnored(room) ? "text-primary opacity-100" : "text-muted-foreground opacity-40",
                )}
                onClick={() => void (isIgnored(room) ? unignore(room) : ignore(room))}
              >
                {isIgnored(room) ? <BellOff className="h-3 w-3" /> : <Bell className="h-3 w-3" />}
              </button>
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
            <details
              className="order-2 mt-1 min-h-0 px-2 text-muted-foreground text-xs md:order-none"
              data-closed-rooms=""
            >
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
              {/*
                A BOTTOM ON THE PILE, and a scroll of its own.
                
                The open list already scrolls inside itself - see the note on
                the nav column, and rooms-scroll-check - and the closed pile did
                not: it rendered every closed room with no bound, so opening it
                pushed the rail's footer, the token box and the log-out below
                the fold. The reader's own list of rooms moved because they
                looked at the rooms they had put away.
                
                MEASURED before choosing this over pagination, which is what the
                row asked for: 29 rooms on this node and 29 buttons in the worst
                case - not a rendering cost, a LAYOUT one. Virtualising 29 items
                would be work with nothing to show; bounding the box fixes what
                actually breaks and keeps working at 300.
                
                40vh rather than a pixel count: the rail is as tall as the
                window and a fixed height is wrong on both a phone and a desk.
              */}
              <div className="max-h-[40vh] overflow-y-auto" data-closed-rooms-list="">
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
              </div>
            </details>
          ) : null}
        </div>

        {/* shrink-0 so the rooms list cannot squeeze it away; mt-auto is gone
            because the rooms block now takes the slack that used to push this
            down. */}
        <div className="order-4 shrink-0 md:order-none">
          <TokenBar />
        </div>
      </aside>

      {/*
        Between the two, and only where there are two: below md the nav is a
        drawer over the page and there is no edge to drag.
      */}
      <div className="hidden md:flex">
        <ResizeHandle
          storageKey="flowy.nav.width"
          min={180}
          max={420}
          edge="left"
          onWidth={setNavWidth}
          label="width of the navigation column"
        />
      </div>

      <main className="flex min-w-0 flex-1 flex-col overflow-hidden pt-12 md:pt-0">
        {/*
          Above every route rather than inside one: a tab running a replaced
          console is a fact about the tab, not about whichever page it happens
          to be showing.
        */}
        <FreshBanner />
        {/*
          And the same argument for the credential: it is a fact about the tab
          rather than about the page, and it is true of every pane at once.
          Below the staleness bar because a tab that is BOTH behind and signed
          out should reload first - the newer console may be the one that can
          tell it so.
        */}
        <CredentialBanner />
        <div className="min-h-0 flex-1 overflow-hidden">{children}</div>
      </main>
    </div>
  );
}
