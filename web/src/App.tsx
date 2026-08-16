import { Route, Routes } from "react-router-dom";

import { Shell } from "@/components/Shell";
import { Activity } from "@/routes/Activity";
import { ArtifactView } from "@/routes/ArtifactView";
import { ChatRoom } from "@/routes/ChatRoom";
import { Direct } from "@/routes/Direct";
import { Home } from "@/routes/Home";
import { Inbox } from "@/routes/Inbox";
import { Metrics } from "@/routes/Metrics";
import { NotFound } from "@/routes/NotFound";
import { Reports } from "@/routes/Reports";
import { TaskView } from "@/routes/TaskView";
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
    <Shell>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/chat/:room" element={<ChatRoom />} />
        {/*
          Not /chat/dm. A direct message is not in a room, so there is no room
          name to put in the path - and a path that looked like a room's would
          be the first place somebody assumed a room could be private.
        */}
        <Route path="/direct" element={<Direct />} />
        <Route path="/inbox" element={<Inbox />} />
        <Route path="/reports" element={<Reports />} />
        <Route path="/worklog" element={<Worklog />} />
        <Route path="/task/:id" element={<TaskView />} />
        <Route path="/p/:project/:type/:id" element={<ArtifactView />} />
        <Route path="/metrics" element={<Metrics />} />
        <Route path="/traces" element={<Traces />} />
        <Route path="/activity" element={<Activity />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
    </Shell>
  );
}
