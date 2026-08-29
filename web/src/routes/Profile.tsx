import { type FormEvent, useCallback, useEffect, useState } from "react";

import { TokenPaste } from "@/components/TokenBar";
import { YourReaders } from "@/components/YourReaders";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { type Me, api } from "@/lib/api";

/**
 * Your own row: the handle people see, and the password you get in with.
 *
 * The operator's words, 2026-08-18: "not doing that cli commands - im logged in
 * via token - give me profile panel - i will change my password here." Every
 * other way to set these is a shell command on the box that runs the node,
 * which is no use to somebody already looking at the console.
 *
 * THE CURRENT VALUE IS RENDERED AS A VALUE. whoami answers ids, so this page
 * asks /api/me, which carries the handle. An empty box cannot tell "no handle
 * is set" from "not loaded yet" - the two want different things from the reader
 * - so the page says which of them is true before it offers a form.
 *
 * WHAT IT ASKS FOR DEPENDS ON HOW YOU GOT HERE, and the node decides that, not
 * this page. A browser on a cookie proves the old password to change it;
 * a bearer token setting a FIRST password has none to prove. has_password and
 * by_bearer come back from the same read, so the field appears when the node
 * would require it and not otherwise - a required field that is not shown is a
 * 400 the reader cannot act on.
 *
 * AND IT SAYS WHAT A SAVE WILL COST BEFORE THE SAVE. A password change ends
 * other sessions; the answer carries sessions_ended and the page reports it.
 * The warning is above the button rather than after it, because "you have been
 * signed out elsewhere" is not news anybody wants after the fact.
 */
/**
 * PASSWORD_MIN and PASSWORD_MAX are store/login.go's, in the units it uses.
 * MAX is a BYTE count because bcrypt truncates at 72 bytes and the node checks
 * len() on the string.
 */
const PASSWORD_MIN = 8;
const PASSWORD_MAX = 72;

export function Profile() {
  const [me, setMe] = useState<Me | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const [handle, setHandle] = useState("");
  // TYPED TWICE, COMPARED HERE. The node cannot catch this one: a typo is a
  // valid password, so every rule it has - see PASSWORD_MIN and PASSWORD_MAX
  // below - passes it. It is discovered at the NEXT LOGIN, and on this node
  // that is worse than usual: the handle is also the login name, there is no
  // "forgot" on /login, and PUT /api/me needs a session or a bearer. So the
  // recovery for one mistyped character is somebody with database access.
  //
  // The operator, seeing the form: "profile panel is funny - no password
  // confirmation, no password strength".
  const [again, setAgain] = useState("");
  const [password, setPassword] = useState("");
  const [current, setCurrent] = useState("");
  const [saved, setSaved] = useState<string | null>(null);
  const [refused, setRefused] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const read = useCallback(async () => {
    setLoading(true);
    try {
      const answer = await api.me();
      setMe(answer);
      setHandle(answer.user.handle ?? "");
      setError(null);
    } catch (err) {
      setMe(null);
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void read();
  }, [read]);

  // THE NODE'S OWN LIMITS, mirrored, and the mirroring is a real cost stated
  // rather than hidden - the same trade MAX_ATTACHMENT makes in lib/api.ts.
  // Two numbers that have to agree, in exchange for the one thing a person
  // notices: a refusal beside the box instead of after a round trip.
  //
  //   login.go:80  fewer than 8   refused
  //   login.go:83  more than 72   refused, because bcrypt reads only the first
  //                72 BYTES and would otherwise verify a truncation - two
  //                different passwords that both work
  //
  // BYTES, NOT CHARACTERS, which is the half a browser gets wrong by default.
  // `"é".length` is 1 and the node counts 2; an eight-emoji password is 8
  // characters and 32 bytes. TextEncoder counts what Go's len() counts, so the
  // box and the door agree about one password rather than two.
  //
  // A cookie session changing a password has to prove the old one. A bearer
  // token does not, and neither does anybody who has no password yet.
  const needsCurrent = Boolean(me?.has_password && !me.by_bearer);
  const passwordBytes = new TextEncoder().encode(password).length;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy || !me) return;

    const body: { handle?: string; password?: string; current?: string } = {};
    const wantHandle = handle.trim();
    if (wantHandle && wantHandle !== (me.user.handle ?? "")) body.handle = wantHandle;
    if (password) {
      // BEFORE THE REQUEST, in the order a person meets the mistakes. The
      // mismatch is checked first because it is the one the node can never
      // answer, and the one that costs a lockout rather than an error message.
      if (again !== password) {
        setRefused("the two passwords are not the same - nothing was saved");
        return;
      }
      if (passwordBytes < PASSWORD_MIN) {
        setRefused(`a password needs at least ${PASSWORD_MIN} characters`);
        return;
      }
      if (passwordBytes > PASSWORD_MAX) {
        setRefused(
          `that is ${passwordBytes} bytes and the limit is ${PASSWORD_MAX} - accents and emoji count for more than one`,
        );
        return;
      }
      body.password = password;
    }
    if (!body.handle && !body.password) {
      setRefused("nothing to save: change the handle or type a new password");
      return;
    }
    if (body.password && needsCurrent) {
      if (!current) {
        setRefused("changing a password from a browser needs the current one");
        return;
      }
      body.current = current;
    }

    setBusy(true);
    setRefused(null);
    setSaved(null);
    try {
      const answer = await api.updateMe(body);
      setPassword("");
      setAgain("");
      setCurrent("");
      // Re-read rather than trusting what was sent: the node's row is the
      // answer, and a page that showed what it posted would look identical
      // whether or not the write took.
      await read();
      const ended = answer.sessions_ended;
      setSaved(
        ended > 0 ? `saved. ${ended} other session${ended === 1 ? "" : "s"} signed out` : "saved",
      );
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
          <h1 className="font-semibold text-base">profile</h1>
          <p className="text-muted-foreground text-xs">
            your handle and your password, on your own row
          </p>
        </div>

        {/*
          THE BEARER TOKEN, WHICH USED TO BE IN THE RAIL on every page. It is a
          thing about WHO YOU ARE, so it belongs on the page about that, next to
          the handle and the password - not in the furniture of every screen.

          Above the handle rather than below it: this is the control that
          DECIDES which row the rest of this page is about, so a reader who has
          the wrong seat finds the fix before the thing that confused them.
        */}
        <TokenPaste />

        {loading ? <p className="text-muted-foreground text-xs">reading your row...</p> : null}

        {error ? (
          <p className="text-destructive text-xs" data-profile-error="">
            {error}
          </p>
        ) : null}

        {me ? (
          <div
            className="flex flex-col gap-1 rounded-md border border-border bg-card p-3 text-xs"
            data-profile-current=""
          >
            <span>
              handle{" "}
              {me.user.handle ? (
                <span className="font-mono" data-profile-handle={me.user.handle}>
                  {me.user.handle}
                </span>
              ) : (
                <span className="text-muted-foreground" data-profile-handle="">
                  not set
                </span>
              )}
            </span>
            <span className="text-muted-foreground">
              id <span className="font-mono">{me.user.id}</span>
            </span>
            <span className="text-muted-foreground" data-profile-haspw={String(me.has_password)}>
              {me.has_password ? "a password is set" : "no password yet"}
              {me.by_bearer ? ", on a bearer token" : ", on a browser session"}
            </span>
          </div>
        ) : null}

        {me ? (
          <form className="flex flex-col gap-3" onSubmit={submit} data-profile-form="">
            <div className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">handle</span>
              <Input
                aria-label="handle"
                data-profile-handle-input=""
                value={handle}
                autoComplete="username"
                spellCheck={false}
                onChange={(e) => setHandle(e.target.value)}
              />
            </div>

            {needsCurrent ? (
              <div className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">current password</span>
                <Input
                  aria-label="current password"
                  data-profile-current-input=""
                  type="password"
                  value={current}
                  autoComplete="current-password"
                  onChange={(e) => setCurrent(e.target.value)}
                />
              </div>
            ) : null}

            <div className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">
                {me.has_password ? "new password" : "password"}
              </span>
              <Input
                aria-label="new password"
                data-profile-password-input=""
                type="password"
                value={password}
                autoComplete="new-password"
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>

            {/*
              THE SECOND BOX. Only once there is something to confirm - an
              empty form with two password fields asks a question nobody has
              started answering yet.
            */}
            {password ? (
              <div className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">and again</span>
                <Input
                  aria-label="repeat the new password"
                  data-profile-password-again=""
                  type="password"
                  value={again}
                  autoComplete="new-password"
                  onChange={(e) => setAgain(e.target.value)}
                />
                {/*
                  SAID WHILE TYPING, not only on submit. A mismatch found at
                  save time is a mismatch found after the reader has stopped
                  looking at the boxes.
                */}
                {again && again !== password ? (
                  <span className="text-destructive" data-profile-password-mismatch="">
                    these do not match
                  </span>
                ) : null}
              </div>
            ) : null}

            {/*
              THE RULE WHERE IT IS TYPED. Both numbers were only discoverable by
              being refused - one at a time, after a round trip.
            */}
            {password ? (
              <p
                className={
                  passwordBytes < PASSWORD_MIN || passwordBytes > PASSWORD_MAX
                    ? "text-destructive text-xs"
                    : "text-muted-foreground text-xs"
                }
                data-profile-password-rule=""
              >
                {passwordBytes < PASSWORD_MIN
                  ? `${PASSWORD_MIN - passwordBytes} more to go - at least ${PASSWORD_MIN}`
                  : passwordBytes > PASSWORD_MAX
                    ? `${passwordBytes} bytes, and ${PASSWORD_MAX} is the limit`
                    : `${passwordBytes} of ${PASSWORD_MAX} bytes`}
              </p>
            ) : null}

            {password ? (
              <p className="text-muted-foreground text-xs" data-profile-warning="">
                changing your password signs your other browsers out
              </p>
            ) : null}

            <Button type="submit" size="sm" disabled={busy} data-profile-save="">
              {busy ? "saving..." : "save"}
            </Button>

            {saved ? (
              <p className="text-xs" data-profile-saved="">
                {saved}
              </p>
            ) : null}
            {refused ? (
              <p className="text-destructive text-xs" data-profile-refused="">
                {refused}
              </p>
            ) : null}
          </form>
        ) : null}

        {/*
          YOUR READERS, on the same page as your handle, because both answer
          "what does this token have on the node". A reader is a durable row
          that outlives the process that made it - three seats found abandoned
          ones from a console load two days earlier - and until now there was
          nowhere at all to look at them.
        */}
        <div className="border-border border-t pt-4">
          <YourReaders />
        </div>
      </div>
    </div>
  );
}
