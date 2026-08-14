import { Link, useLocation } from "react-router-dom";

import { Button } from "@/components/ui/button";

/**
 * The app's own 404. The node answers every non-API path with index.html, so a
 * path that names no route gets here rather than to a server error page - the
 * routing table that decides is this one.
 */
export function NotFound() {
  const { pathname } = useLocation();
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3">
      <div className="font-semibold text-lg">no route for {pathname}</div>
      <p className="max-w-sm text-center text-muted-foreground text-sm">
        try /chat/general, /p/&lt;project&gt;/&lt;type&gt;/&lt;id&gt; or /metrics
      </p>
      <Button asChild variant="secondary">
        <Link to="/">back to the overview</Link>
      </Button>
    </div>
  );
}
