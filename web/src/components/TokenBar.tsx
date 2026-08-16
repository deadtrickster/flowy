import { useState } from "react";

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
  const { token, whoami, error, loading, signIn } = useSession();
  const [draft, setDraft] = useState(token);

  return (
    <form
      className="flex flex-col gap-2 rounded-md border border-border bg-card p-2"
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
    </form>
  );
}
