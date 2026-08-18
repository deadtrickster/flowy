import { type FormEvent, useState } from "react";
import { useNavigate } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useSession } from "@/lib/session";

/**
 * Where a person gets in.
 *
 * The operator's words were "i dont want to bother with token. token is for
 * api, not for me" - so this is a handle and a password, and the bearer box
 * stays exactly where it was for the seats that need one. Two credentials, and
 * the console does not prefer either: whoami answers for both.
 *
 * NOTHING IS STORED HERE. The node's cookie is httpOnly, so no script can read
 * it and there is nothing for this page to keep. Being signed in is whoami
 * answering, asked again after a login rather than inferred from its 200 - a
 * console that believed the login instead of the node would show a signed-in
 * screen against a session the node has already ended.
 *
 * THE REFUSAL IS THE NODE'S SENTENCE. It says one thing for a wrong handle and
 * a wrong password on purpose, because which of the two was wrong is an oracle
 * for which accounts exist, and this page must not improve on it. The rate
 * limit's 429 is shown the same way for the same reason.
 *
 * AND THE LINE ABOUT PASSWORDS IS NOT DECORATION. Nobody has one until the
 * operator runs `flowy passwd` at the shell, so every correct-looking login on
 * a fresh node is a 401 - and "handle or password is wrong" is useless to
 * somebody who has never had a password. There is no API answer that separates
 * those without becoming that same oracle, so the page says where a password
 * comes from instead of guessing why the login failed.
 */
export function Login() {
  const { whoami, logIn, logOut } = useSession();
  const navigate = useNavigate();
  const [handle, setHandle] = useState("");
  const [password, setPassword] = useState("");
  const [refused, setRefused] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy || !handle.trim() || !password) return;
    setBusy(true);
    setRefused(null);
    try {
      await logIn(handle.trim(), password);
      setPassword("");
      navigate("/");
    } catch (err) {
      setRefused((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col overflow-y-auto p-6">
      <div className="flex w-full max-w-sm flex-col gap-4">
        <div className="flex flex-col gap-1">
          <h1 className="font-semibold text-base">log in</h1>
          <p className="text-muted-foreground text-xs">
            a person signs in here; a seat carries a bearer token instead
          </p>
        </div>

        {whoami ? (
          /*
            Already in. Said as who the NODE says you are rather than as a
            flag - the two disagree exactly when a session has ended, which is
            the case this page exists for.
          */
          <div className="flex flex-col gap-2 rounded-md border border-border bg-card p-3 text-xs">
            {/*
              The id rather than a name, because whoami answers ids: the handle
              belongs to the registry and this page is not the place to go and
              look one up. It says WHO THE NODE THINKS YOU ARE, which is the
              only claim it can make honestly.
            */}
            <span data-login-as={whoami.agent ?? whoami.user}>
              signed in as <span className="font-mono">{whoami.agent ?? whoami.user}</span>
            </span>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              data-logout=""
              onClick={() => {
                void logOut();
              }}
            >
              log out
            </Button>
          </div>
        ) : null}

        <form className="flex flex-col gap-3" onSubmit={submit} data-login-form="">
          <div className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">handle</span>
            <Input
              aria-label="handle"
              data-login-handle=""
              value={handle}
              autoComplete="username"
              spellCheck={false}
              onChange={(e) => setHandle(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">password</span>
            <Input
              aria-label="password"
              data-login-password=""
              type="password"
              value={password}
              autoComplete="current-password"
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>

          {refused ? (
            <p data-login-refused="" className="text-destructive text-xs">
              {refused}
            </p>
          ) : null}

          <Button type="submit" disabled={busy || !handle.trim() || !password} data-login-submit="">
            {busy ? "logging in…" : "log in"}
          </Button>
        </form>

        <p className="text-muted-foreground text-xs">
          a password is set at the shell, with <code>flowy passwd --handle you</code> - this page
          cannot make one, and there is no signup here.
        </p>
      </div>
    </div>
  );
}
