// The UI is normally served by the Go binary, so the API and WebSocket share
// its origin. VITE_API_URL and VITE_WS_URL point a separately hosted UI at
// another origin.
export const API_BASE = import.meta.env.VITE_API_URL ?? "/api/v1";

export function websocketURL() {
  if (import.meta.env.VITE_WS_URL) return import.meta.env.VITE_WS_URL as string;
  const { protocol, host } = window.location;
  return `${protocol === "https:" ? "wss" : "ws"}://${host}/ws`;
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code = "",
  ) {
    super(message);
  }
}

export async function request<T>(path: string, init?: RequestInit & { token?: string }): Promise<T> {
  const { token, ...rest } = init ?? {};
  let response: Response;
  try {
    response = await fetch(`${API_BASE}${path}`, {
      ...rest,
      credentials: "include",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...rest.headers,
      },
    });
  } catch {
    throw new ApiError("Cannot reach the exchange. It may be waking up; retrying shortly.", 0, "OFFLINE");
  }
  if (response.status === 204) return undefined as T;
  // A sleeping host's proxy answers with an HTML page, not our JSON.
  const payload = await response.json().catch(() => null);
  if (!response.ok || payload === null) {
    throw new ApiError(
      payload?.error?.message ?? "The exchange is starting up or unavailable. Retrying shortly.",
      response.status,
      payload?.error?.code ?? (payload === null ? "UNAVAILABLE" : ""),
    );
  }
  return payload as T;
}
