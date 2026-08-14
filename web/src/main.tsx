import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";

import App from "@/App";
import { SessionProvider } from "@/lib/session";
import "@/index.css";

const container = document.getElementById("root");
if (!container) {
  throw new Error("index.html is missing #root");
}

// BrowserRouter, not HashRouter: the routes are real paths, the node serves
// index.html for every one of them, and a link to /chat/general is a link
// somebody can send to somebody else.
createRoot(container).render(
  <StrictMode>
    <BrowserRouter>
      <SessionProvider>
        <App />
      </SessionProvider>
    </BrowserRouter>
  </StrictMode>,
);
