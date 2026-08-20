import { useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useSession } from "@/lib/session";
import { shortId } from "@/lib/utils";

/**
 * The dev token, pasted in and kept in localStorage. It is a bearer token: the
 * node hands it out, this console only carries it, and everything the console
 * can see is whatever that token resolves to on the other end.
 */
export function TokenBar() {
  const { token, whoami, error, loading, signIn, logOut } = useSession();
  const [draft, setDraft] = useState(token);

  return (
    <form
      // Named so a check can ask where it IS, not only that it exists. The
      // rooms list pushed this off the bottom of the page before it scrolled
      // inside itself, and "the token bar is in the DOM" was true throughout.
      data-token-bar=""
      className="flex flex-col gap-2 rounded-md border border-border bg-card p-2"
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
      <div className="flex items-center justify-between gap-2">
        <Button type="submit" size="sm" variant="secondary">
          use
        </Button>
        {loading ? (
          <span className="text-muted-foreground text-xs">resolving…</span>
        ) : whoami ? (
          <Badge variant={whoami.agent ? "agent" : "human"}>
            {whoami.agent ? "agent" : "user"} {shortId(whoami.agent ?? whoami.user)}
          </Badge>
        ) : (
          <Badge variant="outline">{token ? "rejected" : "signed out"}</Badge>
        )}
      </div>
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
      {error ? <div className="text-destructive text-xs">{error}</div> : null}
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
    </form>
  );
}
