import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { watchCredential } from "@/lib/api";

/**
 * Says the credential is dead, because nothing else on the page does.
 *
 * 01M0K76WY4. The operator reported the console "stopped working". It was
 * working: every read was answering 401, every pane was drawing its own empty
 * state, and the frame around them looked exactly as it always does. Measured
 * against the live node with a rejected credential - /memory went from 11521
 * characters to 454, the sidebar's thirty rooms to none, and not one word
 * anywhere said why.
 *
 * A BAR, NOT A TOAST, for the reason FreshBanner is one: a toast disappears,
 * and this is a condition that persists until somebody signs in. It sits above
 * the routes rather than inside any of them because it is true of all of them
 * at once - a message per empty pane would be thirteen copies of one fact.
 *
 * IT DOES NOT SAY WHY, and that is not vagueness. The session behind the
 * original report had not expired; an EARLIER one had, and login.go:488 swept
 * the row while the browser kept the cookie. Expired and swept give the
 * identical 401 and the browser cannot see which - so this says the part that
 * is true either way, and leaves the diagnosis to somebody who can read the
 * table.
 */
export function CredentialBanner() {
  const [dead, setDead] = useState(false);

  useEffect(() => watchCredential(setDead), []);

  if (!dead) return null;

  return (
    <div
      data-credential-dead=""
      className="flex items-center gap-3 border-destructive/40 border-b bg-destructive/10 px-4 py-2 text-destructive text-xs"
    >
      <span>
        this credential is no longer valid, so every panel here is empty for that reason and not
        because the node is. Sign in again, or paste a working token in the rail.
      </span>
      <Link
        className="ml-auto rounded border border-destructive/40 px-2 py-0.5 hover:bg-destructive/20"
        to="/login"
      >
        sign in
      </Link>
    </div>
  );
}
