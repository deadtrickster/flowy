import { Route, Routes } from "react-router-dom";

import { Shell } from "@/components/Shell";
import { ArtifactView } from "@/routes/ArtifactView";
import { ChatRoom } from "@/routes/ChatRoom";
import { Home } from "@/routes/Home";
import { Inbox } from "@/routes/Inbox";
import { Metrics } from "@/routes/Metrics";
import { NotFound } from "@/routes/NotFound";
import { TaskView } from "@/routes/TaskView";

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
        <Route path="/inbox" element={<Inbox />} />
        <Route path="/task/:id" element={<TaskView />} />
        <Route path="/p/:project/:type/:id" element={<ArtifactView />} />
        <Route path="/metrics" element={<Metrics />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
    </Shell>
  );
}
