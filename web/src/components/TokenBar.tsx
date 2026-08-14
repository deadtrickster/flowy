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
        <div className="text-muted-foreground text-xs">project {whoami.project}</div>
      ) : null}
      {error ? <div className="text-destructive text-xs">{error}</div> : null}
    </form>
  );
}
