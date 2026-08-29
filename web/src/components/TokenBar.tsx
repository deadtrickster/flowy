import { useState } from "react";
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useSession } from "@/lib/session";

/**
 * The dev token, pasted in and kept in localStorage. It is a bearer token: the
 * node hands it out, this console only carries it, and everything the console
 * can see is whatever that token resolves to on the other end.
 */
/**
 * WHO YOU ARE AND HOW TO LEAVE. This is the furniture half, and it stays in the
 * rail on every page.
 *
 * THE PASTE-A-TOKEN BOX IS NOT HERE ANY MORE - see TokenPaste, on /profile. The
 * operator: "yeah move it in profile. I dont use it and it wastes time." What
 * they meant is the dev-token input; moving the WHOLE bar took the log-in link
 * and the log-out button with it, and the console had no way in or out on any
 * page. Three checks caught it - this component's own note had said so:
 * "being unable to LEAVE is the same defect one step later".
 */
export function TokenBar() {
  const { whoami, logOut } = useSession();

  return (
    <div
      // Named so a check can ask where it IS, not only that it exists. The
      // rooms list pushed this off the bottom of the page before it scrolled
      // inside itself, and "the token bar is in the DOM" was true throughout.
      data-token-bar=""
      className="flex flex-col gap-2 rounded-md border border-border bg-card p-2"
    >
      {whoami?.project ? (
        whoami.project_fixture ? (
          /*
           * The current-project indicator, and the one case it has to shout.
           * Everything written with this token lands in this project, and a
           * fixture project is the smoke seeder's demo data - writable, valid,
           * and not where real work belongs. A day of it went into `pa` because
           * this line said "project pa" and nothing else.
           */
          <div className="font-medium text-destructive text-xs">
            writing into {whoami.project} - a FIXTURE project (demo seed data)
          </div>
        ) : (
          <div className="text-muted-foreground text-xs">writing into {whoami.project}</div>
        )
      ) : null}
      {/*
        THE WAY IN, WHERE SOMEBODY LOOKS FOR IT.
        The operator, blocked: "fyi, i cant login because there is no login
        page". There is - App.tsx routes /login and it renders - and NOTHING IN
        THE CONSOLE LINKED TO IT. Measured: zero occurrences of to="/login"
        across web/src. A page reachable only by typing its URL is a page the
        person it was built for does not have.
        Login.tsx's own comment carries why it exists: "i dont want to bother
        with token. token is for api, not for me". So the box above stays for
        the seats that need it, and this is the door for the person - two
        credentials, and the console prefers neither.
        Both directions, because being unable to LEAVE is the same defect one
        step later: the link says log in when nobody is signed in, and log out
        when somebody is.
      */}
      {whoami ? (
        <button
          type="button"
          data-log-out=""
          className="cursor-pointer text-left text-muted-foreground text-xs underline decoration-dotted hover:text-foreground"
          onClick={() => void logOut()}
        >
          log out
        </button>
      ) : (
        <Link
          to="/login"
          data-log-in=""
          className="text-muted-foreground text-xs underline decoration-dotted hover:text-foreground"
        >
          log in with a handle and password
        </Link>
      )}
    </div>
  );
}

/**
 * THE DEV TOKEN, PASTED IN. On /profile, and nowhere else.
 *
 * It sat at the foot of the left rail on every page of the console with the
 * bearer token visible in an input. The operator: "yeah move it in profile. I
 * dont use it and it wastes time." It is a thing about WHO YOU ARE, so it
 * belongs on the page about that.
 *
 * SEPARATE FROM TokenBar ON PURPOSE. The first attempt moved the whole bar, and
 * the bar also holds the log-in link and the log-out button - so the console
 * briefly had no way in and no way out on any page. Two different things had
 * been living in one box: the furniture a person needs everywhere, and a
 * credential field for the seats that use one.
 *
 * Login.tsx's own note is the other half of why both exist: "i dont want to
 * bother with token. token is for api, not for me". Two credentials, and the
 * console prefers neither.
 */
export function TokenPaste() {
  const { token, error, loading, signIn } = useSession();
  const [draft, setDraft] = useState(token);

  return (
    <form
      data-token-paste=""
      className="flex flex-col gap-2 rounded-md border border-border bg-card p-3"
      autoComplete="off"
      onSubmit={(event) => {
        event.preventDefault();
        signIn(draft);
      }}
    >
      <label className="font-medium text-muted-foreground text-xs" htmlFor="token">
        bearer token
      </label>
      <Input
        id="token"
        value={draft}
        placeholder="paste a dev token"
        autoComplete="off"
        spellCheck={false}
        onChange={(event) => setDraft(event.target.value)}
      />
      <div className="flex items-center gap-2">
        <Button type="submit" size="sm" variant="secondary">
          use
        </Button>
        {loading ? <span className="text-muted-foreground text-xs">resolving…</span> : null}
        {error ? <span className="text-destructive text-xs">{error}</span> : null}
      </div>
    </form>
  );
}
