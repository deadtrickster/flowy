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
        if (!stopped) setLoading(false);
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
