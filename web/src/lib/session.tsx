import { type ReactNode, createContext, useCallback, useContext, useEffect, useState } from "react";

import { type Whoami, api, getToken, setToken } from "@/lib/api";

/**
 * The session is the bearer token and whatever the node says it resolves to.
 *
 * There is no login: a token is minted by the node's operator and pasted in
 * here, and the console's whole idea of who it is comes back from
 * /api/whoami. Nothing is inferred locally - a token the node has never heard
 * of leaves the console signed out, which is the same thing the API says.
 */
interface Session {
  token: string;
  whoami: Whoami | null;
  error: string | null;
  loading: boolean;
  signIn: (token: string) => void;
}

const SessionContext = createContext<Session | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [token, setStateToken] = useState(() => getToken());
  const [whoami, setWhoami] = useState<Whoami | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!token) {
      setWhoami(null);
      setError(null);
      return;
    }
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
  }, [token]);

  const signIn = useCallback((next: string) => {
    setToken(next.trim());
    setStateToken(next.trim());
  }, []);

  return (
    <SessionContext.Provider value={{ token, whoami, error, loading, signIn }}>
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
