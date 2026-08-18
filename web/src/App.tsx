import { Route, Routes } from "react-router-dom";

import { Shell } from "@/components/Shell";
import { UnreadProvider } from "@/lib/unread";
import { Activity } from "@/routes/Activity";
import { ArtifactView } from "@/routes/ArtifactView";
import { ChatRoom } from "@/routes/ChatRoom";
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
import { Reports } from "@/routes/Reports";
import { TaskView } from "@/routes/TaskView";
import { Todos } from "@/routes/Todos";
import { Traces } from "@/routes/Traces";
import { Worklog } from "@/routes/Worklog";

/**
 * Every view is a path, and every path is a bookmark. The node serves
 * index.html for all of them, so a reload of /chat/general lands back on the
 * room rather than on a 404 - which is the difference between a console and a
 * page with tabs.
 */
export default function App() {
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
          <Route path="/new" element={<NewEntity />} />
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
          A shape inside one. (project, type, id) addresses every artifact in
          this store; a cell is that plus the mxCell id, which is the id that
          survives a re-layout - a coordinate does not. The view says which
          shape is meant, and says so loudly when the shape is gone.
        */}
          <Route path="/diagrams/:id/:cell" element={<DiagramView />} />
          <Route path="/worklog" element={<Worklog />} />
          <Route path="/task/:id" element={<TaskView />} />
          <Route path="/p/:project/:type/:id" element={<ArtifactView />} />
          <Route path="/metrics" element={<Metrics />} />
          <Route path="/traces" element={<Traces />} />
          <Route path="/activity" element={<Activity />} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </Shell>
    </UnreadProvider>
  );
}
