import { Route, Routes } from "react-router-dom";

import { Shell } from "@/components/Shell";
import { useRowLinkClicks } from "@/lib/rowlink";
import { UnreadProvider } from "@/lib/unread";
import { Activity } from "@/routes/Activity";
import { ArtifactView } from "@/routes/ArtifactView";
import { Bookmarks } from "@/routes/Bookmarks";
import { ChatRoom } from "@/routes/ChatRoom";
import { DashboardView } from "@/routes/DashboardView";
import { Dashboards } from "@/routes/Dashboards";
import { DiagramView } from "@/routes/DiagramView";
import { Diagrams } from "@/routes/Diagrams";
import { Direct } from "@/routes/Direct";
import { Findings } from "@/routes/Findings";
import { Home } from "@/routes/Home";
import { Inbox } from "@/routes/Inbox";
import { Login } from "@/routes/Login";
import { Memory } from "@/routes/Memory";
import { Metrics } from "@/routes/Metrics";
import { NewEntity } from "@/routes/NewEntity";
import { NotFound } from "@/routes/NotFound";
import { Openspec } from "@/routes/Openspec";
import { OpenspecView } from "@/routes/OpenspecView";
import { Profile } from "@/routes/Profile";
import { Projects } from "@/routes/Projects";
import { Reports } from "@/routes/Reports";
import { Skills } from "@/routes/Skills";
import { TaskView } from "@/routes/TaskView";
import { Todos } from "@/routes/Todos";
import { Traces } from "@/routes/Traces";
import { Vms } from "@/routes/Vms";
import { Worklog } from "@/routes/Worklog";

/**
 * Every view is a path, and every path is a bookmark. The node serves
 * index.html for all of them, so a reload of /chat/general lands back on the
 * room rather than on a 404 - which is the difference between a console and a
 * page with tabs.
 */
export default function App() {
  // Row-id links resolve on click through the console's own credential - see
  // lib/rowlink for why the resolver route alone cannot serve a token seat.
  useRowLinkClicks();
  return (
    // Outside the shell because both sides of it need the same numbers: the
    // sidebar draws the badges and the room clears them, and a count owned by
    // either one alone is the per-tab latch this console does not keep.
    <UnreadProvider>
      <Shell>
        <Routes>
          <Route path="/" element={<Home />} />
          <Route path="/chat/:room" element={<ChatRoom />} />
          {/*
          THE SIDE COLUMN'S TAB IS IN THE PATH, so a pane is a place. It was
          component state, which meant the back button did nothing after
          choosing one and a person could not send anybody the pane they were
          looking at - the same gap /todos/merge already closed for the queue
          page, and the same one the diagram row needs one level deeper before
          a shape can be cited.

          A segment rather than a query string: this is which of five things
          the column is showing, not a filter over one of them.
        */}
          <Route path="/chat/:room/:pane" element={<ChatRoom />} />
          {/*
          And one message inside it, which is the pointer a citation already
          travels as - see lib/cite. "Look at what X said" was a screenshot
          before this: the selected message was component state, so the thread
          on screen could not be sent to anybody.
        */}
          <Route path="/chat/:room/thread/:message" element={<ChatRoom />} />
          {/*
          Not /chat/dm. A direct message is not in a room, so there is no room
          name to put in the path - and a path that looked like a room's would
          be the first place somebody assumed a room could be private.
        */}
          <Route path="/direct" element={<Direct />} />
          <Route path="/inbox" element={<Inbox />} />
          {/*
          /todos was a per-project page and was deleted for being a second list
          that disagreed with the room's panel. This is not that page: it is the
          queue ACROSS projects, and it says which reader's union it is. A
          project-scoped list here would be the old mistake with a new route.
        */}
          {/*
          The one place a person makes a row. One list, closed, the resolved
          types - see NewEntity for why offering both spellings of identity
          would make the ambiguity permanent.
        */}
          {/*
          Where a person gets in. A seat's bearer token is pasted into the rail
          and a person's session is a cookie the node sets - two credentials,
          one answer from whoami.
        */}
          <Route path="/login" element={<Login />} />
          <Route path="/profile" element={<Profile />} />
          <Route path="/new" element={<NewEntity />} />
          {/*
          WHICH PROJECT YOU ARE IN, and what else exists. Two doors and no
          surface until now: lib/api.ts called GET /api/projects and nothing
          drew the answer. See routes/Projects.tsx for what the page can and
          cannot say.
        */}
          <Route path="/projects" element={<Projects />} />
          <Route path="/todos" element={<Todos />} />
          {/*
          The merge tab was a useState in Todos.tsx until now, which made
          "opened the merge queue" invisible to the URL: reload or back landed
          on the todo list with no trace the merge view had ever been open.
          Same component, second path - it reads which tab from the route it
          was given rather than from its own state.
        */}
          <Route path="/todos/merge" element={<Todos />} />
          <Route path="/memory" element={<Memory />} />
          <Route path="/reports" element={<Reports />} />
          <Route path="/findings" element={<Findings />} />
          {/*
            A diagram is a document with an id, so it is a path and not a
            modal: a drawing somebody wants a second opinion on is a link they
            can paste into the room.
          */}
          <Route path="/diagrams" element={<Diagrams />} />
          <Route path="/diagrams/:id" element={<DiagramView />} />
          {/*
            A dashboard is a declaration, not a program: agents author the
            row through the artifact door, and these pages render whatever
            the tiles declare over the metric rows producers pushed. See
            routes/Dashboards.tsx and routes/DashboardView.tsx.
          */}
          <Route path="/dashboards" element={<Dashboards />} />
          <Route path="/dashboards/:id" element={<DashboardView />} />
          {/*
          A shape inside one. (project, type, id) addresses every artifact in
          this store; a cell is that plus the mxCell id, which is the id that
          survives a re-layout - a coordinate does not. The view says which
          shape is meant, and says so loudly when the shape is gone.
        */}
          <Route path="/diagrams/:id/:cell" element={<DiagramView />} />
          {/*
            The skills shelf. A skill is a memory row of kind "skill" - the
            list lives here, and the row itself opens on the ordinary artifact
            page, where the body renders as markdown. See routes/Skills.tsx.
          */}
          <Route path="/skills" element={<Skills />} />
          {/*
            The openspec board: the spec and change rows, and one row's files,
            verdict, derived todos and clashes. The rows ARE artifacts - the
            /p/ paths still open them - but a change is a directory of markdown
            files in fields, and that shape wants a page of its own. See
            routes/OpenspecView for what it draws.
          */}
          <Route path="/openspec" element={<Openspec />} />
          <Route path="/openspec/:id" element={<OpenspecView />} />
          <Route path="/bookmarks" element={<Bookmarks />} />
          <Route path="/worklog" element={<Worklog />} />
          <Route path="/task/:id" element={<TaskView />} />
          <Route path="/p/:project/:type/:id" element={<ArtifactView />} />
          <Route path="/metrics" element={<Metrics />} />
          <Route path="/traces" element={<Traces />} />
          <Route path="/activity" element={<Activity />} />
          {/*
            Reachable by URL and from the rail. A panel with a route and no rail
            entry is one the operator who asked for it has to be told about, and
            the ask was "right from flow".
          */}
          <Route path="/vms" element={<Vms />} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </Shell>
    </UnreadProvider>
  );
}
