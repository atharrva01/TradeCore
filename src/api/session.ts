import { create } from "zustand";
import { ApiError, request } from "./client";
import { queryClient } from "./queryClient";
import type { Me, Session } from "./types";

const TOKEN_KEY = "tradecore-session";

const readToken = () => {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
};

const writeToken = (token: string) => {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    // Private mode: the session still works for this tab.
  }
};

type SessionState = {
  token: string;
  user: Me | null;
  /** "loading" until the first restore attempt finishes. */
  status: "loading" | "anonymous" | "signed-in";
};

export const useSession = create<SessionState>(() => ({ token: readToken(), user: null, status: "loading" }));

async function adopt(session: Session) {
  writeToken(session.accessToken);
  useSession.setState({ token: session.accessToken, status: "signed-in" });
  const me = await request<Me>("/me", { token: session.accessToken });
  useSession.setState({ user: me });
  return session.accessToken;
}

function signOutLocally() {
  writeToken("");
  useSession.setState({ token: "", user: null, status: "anonymous" });
  queryClient.removeQueries({ queryKey: ["me"] });
  queryClient.removeQueries({ queryKey: ["portfolio"] });
  queryClient.removeQueries({ queryKey: ["orders"] });
  queryClient.removeQueries({ queryKey: ["myTrades"] });
}

let renewing: Promise<string> | null = null;

/** Exchanges the refresh cookie for a new access token; "" when there is none. */
export function renewSession(): Promise<string> {
  renewing ??= request<Session>("/auth/refresh", { method: "POST" })
    .then(adopt)
    .catch(() => {
      signOutLocally();
      return "";
    })
    .finally(() => {
      renewing = null;
    });
  return renewing;
}

/** Restores the session on load: the stored token, else the refresh cookie. */
export async function restoreSession() {
  const token = useSession.getState().token;
  if (token) {
    try {
      const me = await request<Me>("/me", { token });
      useSession.setState({ user: me, status: "signed-in" });
      return;
    } catch (error) {
      if (!(error instanceof ApiError) || error.status !== 401) {
        // The server is unreachable; keep the token and let queries retry.
        useSession.setState({ status: "signed-in" });
        return;
      }
    }
  }
  await renewSession();
}

let creatingGuest: Promise<string> | null = null;

export function startGuestSession(): Promise<string> {
  creatingGuest ??= request<Session>("/auth/guest", { method: "POST", body: JSON.stringify({ name: "Guest Trader" }) })
    .then(adopt)
    .finally(() => {
      creatingGuest = null;
    });
  return creatingGuest;
}

/** Returns a usable token, creating a guest account when signed out. */
export async function ensureSession(): Promise<string> {
  const { token, status } = useSession.getState();
  if (token && status !== "anonymous") return token;
  return startGuestSession();
}

export async function signIn(email: string, password: string) {
  return adopt(await request<Session>("/auth/login", { method: "POST", body: JSON.stringify({ email, password }) }));
}

export async function signUp(email: string, password: string, displayName: string) {
  return adopt(
    await request<Session>("/auth/signup", { method: "POST", body: JSON.stringify({ email, password, displayName }) }),
  );
}

export async function signOut() {
  await request("/auth/logout", { method: "POST" }).catch(() => undefined);
  signOutLocally();
}

/** Calls the API as the signed-in user, renewing an expired token once. */
export async function authed<T>(path: string, init?: RequestInit): Promise<T> {
  const token = useSession.getState().token;
  try {
    return await request<T>(path, { ...init, token });
  } catch (error) {
    if (!(error instanceof ApiError) || error.status !== 401) throw error;
    const renewed = await renewSession();
    if (!renewed) throw error;
    return request<T>(path, { ...init, token: renewed });
  }
}
