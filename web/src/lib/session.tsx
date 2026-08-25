import { type ReactNode, createContext, useCallback, useContext, useEffect, useState } from "react";

import { type Whoami, api, getToken, setToken } from "@/lib/api";

/**
 * The session is whatever the node says this browser is, however it said it.
 *
 * TWO CREDENTIALS, ONE ANSWER. A seat carries a bearer token, pasted in here
 * and kept in localStorage. A person logs in and the node sets an httpOnly
 * cookie, which nothing in this console can read and nothing here stores. Both
 * arrive at /api/whoami, and whoami answering is the console's whole idea of
 * being signed in - a page keeping its own flag disagrees with the node the
 * first time a session ends, and the disagreement is invisible until somebody
 * tries to write.
 *
 * So whoami is asked even with NO token, which it was not before: a cookie
 * authenticates a browser that has never pasted anything, and a console that
 * only asked when it held a token would show a logged-in person the signed-out
 * screen.
 */
interface Session {
  token: string;
  whoami: Whoami | null;
  error: string | null;
  loading: boolean;
  signIn: (token: string) => void;
  /** Log a person in by handle and password; the node answers with a cookie. */
  logIn: (handle: string, password: string) => Promise<void>;
  /** And out, which ends the session at the node rather than forgetting it here. */
  logOut: () => Promise<void>;
  /**
   * Ask the node again who this is.
   *
   * For acts that change what whoami ANSWERS without changing the credential -
   * entering a project is the first of them. The session cannot notice that on
   * its own: nothing about the token or the cookie moved, only the row behind
   * it, and a console that kept its own copy of "which project" would disagree
   * with the node from the moment a switch was refused.
   */
  refresh: () => void;
}

const SessionContext = createContext<Session | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [token, setStateToken] = useState(() => getToken());
  const [whoami, setWhoami] = useState<Whoami | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  // Whether the FIRST whoami has come back. Its own flag rather than
  // (whoami === null && loading), because those two are false together both
  // before the effect runs and after a genuine signed-out answer, and telling
  // those apart is the entire point.
  const [settling, setSettling] = useState(true);

  // Bumped whenever the credential changes in a way the node decides - a login
  // or a logout - so the ask below runs again without this file keeping its own
  // copy of the answer.
  const [asked, setAsked] = useState(0);
  // The same trigger the login and logout paths use, exposed: see refresh in
  // the interface above.
  const refresh = useCallback(() => setAsked((n) => n + 1), []);

  // token and asked are TRIGGERS, not reads. The request carries whichever
  // credential the browser has - the bearer through authHeader, the session
  // cookie by itself - so the body names neither, and these two say WHEN to ask
  // the node again: a token was pasted, or somebody logged in or out. Dropping
  // them leaves the console showing the previous answer.
  // biome-ignore lint/correctness/useExhaustiveDependencies: both are triggers rather than reads - see above
  useEffect(() => {
    let stopped = false;
    setLoading(true);
    api
      .whoami()
      .then((who) => {
        if (stopped) return;
        setWhoami(who);
        setError(null);
      })
      .catch((err: Error) => {
        if (stopped) return;
        setWhoami(null);
        setError(err.message);
      })
      .finally(() => {
        if (stopped) return;
        setLoading(false);
        setSettling(false);
      });
    return () => {
      stopped = true;
    };
  }, [token, asked]);

  const signIn = useCallback((next: string) => {
    setToken(next.trim());
    setStateToken(next.trim());
  }, []);

  const logIn = useCallback(async (handle: string, password: string) => {
    await api.login(handle, password);
    // Ask the node who that made us rather than assuming it worked: the cookie
    // is httpOnly and unreadable here, so the login's own 200 is the only thing
    // this console could believe, and believing it is how a page ends up
    // signed in against a node that disagrees.
    setAsked((n) => n + 1);
  }, []);

  const logOut = useCallback(async () => {
    await api.logout();
    setAsked((n) => n + 1);
  }, []);

  // NOTHING CREDENTIAL-SHAPED IS PAINTED BEFORE THE CREDENTIAL IS KNOWN.
  //
  // whoami arrives over the network; a token was read out of localStorage
  // synchronously. So the moment the pages below stopped asking `token` and
  // started asking whoami - which is the point of this change - they gained a
  // first frame in which nobody is signed in yet, and every one of them says so
  // in words: "log in, or paste a token, to read the worklog". A person who IS
  // signed in was shown the signed-out screen for the length of one round trip,
  // on every page load.
  //
  // Caught by person-sees-the-console-check.mjs on its first run, which is the
  // whole argument for having written it: the sweep was correct and introduced
  // this, and no amount of reading the diff would have shown it.
  //
  // "Not asked yet" is a third state and it is not "signed out". This holds the
  // tree until the first answer settles - once, not on every refresh, because
  // later asks have a previous answer to keep showing and only the first has
  // nothing. The alternative was a fourth argument threaded through two dozen
  // empty-state helpers, which is the shape that produced the original defect.
  if (settling) {
    return (
      <SessionContext.Provider
        value={{ token, whoami, error, loading, signIn, logIn, logOut, refresh }}
      >
        <div className="p-6 text-muted-foreground text-sm" data-session-settling>
          asking the node who you are…
        </div>
      </SessionContext.Provider>
    );
  }

  return (
    <SessionContext.Provider
      value={{ token, whoami, error, loading, signIn, logIn, logOut, refresh }}
    >
      {children}
    </SessionContext.Provider>
  );
}

export function useSession(): Session {
  const session = useContext(SessionContext);
  if (!session) {
    throw new Error("useSession must be used inside a SessionProvider");
  }
  return session;
}

/**
 * Whether anybody is signed in - which is whoami answering, not a bearer token
 * sitting in localStorage.
 *
 * IT EXISTS AS A HOOK RATHER THAN AS `whoami != null` IN TWENTY-FIVE FILES
 * because that is exactly how the previous rule spread and then rotted. Every
 * page reached for `token` and meant "is anybody here", each one defensibly,
 * and the day a second way to be somebody arrived - a session cookie, so a
 * person need not hold a token - all twenty-five were wrong at once and nothing
 * connected them. The operator found it as a chat box that would not accept
 * typing: "cant post to chat - that red cirlce pointer".
 *
 * A named hook makes the next such change one edit rather than twenty-five, and
 * makes the question askable: `grep useSignedIn` says which surfaces gate on
 * being signed in, which `grep token` never could, because token is also a
 * value that three files legitimately handle.
 *
 * NOT THE SAME QUESTION AS "MAY I WRITE HERE". A write lands in the principal's
 * home project, so a person with a session and no project entered has nowhere
 * to put one - see ChatRoom's cannotPost, which asks both and says which is
 * missing. This hook answers only the first.
 */
export function useSignedIn(): boolean {
  return useSession().whoami != null;
}
