import { type FormEvent, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { API_BASE } from "../api/client";
import { useHealth } from "../api/queries";
import { signIn, signUp, startGuestSession, useSession } from "../api/session";

export function AuthPage() {
  const [params] = useSearchParams();
  const [mode, setMode] = useState<"login" | "signup">(params.get("mode") === "signup" ? "signup" : "login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState(params.get("error") ? "Google sign-in did not complete. Please try again." : "");
  const [busy, setBusy] = useState(false);
  const navigate = useNavigate();
  const health = useHealth();
  const isGuest = useSession((state) => state.user?.isGuest);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (mode === "signup") await signUp(email, password, name);
      else await signIn(email, password);
      navigate("/trade/ATHR");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Something went wrong");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="auth-page">
      <form className="panel auth-card" onSubmit={submit}>
        <div className="segmented wide" role="tablist">
          <button type="button" role="tab" aria-selected={mode === "login"} className={mode === "login" ? "active" : ""} onClick={() => setMode("login")}>
            Log in
          </button>
          <button type="button" role="tab" aria-selected={mode === "signup"} className={mode === "signup" ? "active" : ""} onClick={() => setMode("signup")}>
            Sign up
          </button>
        </div>
        <h1>{mode === "signup" ? "Create your trading account" : "Welcome back"}</h1>
        <p className="muted">
          {mode === "signup"
            ? "New accounts start with ₹10,00,000 of virtual cash and appear on the leaderboard."
            : "Log in to pick up where you left off."}
          {isGuest && mode === "signup" && " Guest balances are not carried over."}
        </p>
        {mode === "signup" && (
          <label className="field-label">
            Display name
            <div className="input-wrap">
              <input value={name} onChange={(event) => setName(event.target.value)} maxLength={32} required autoComplete="nickname" />
            </div>
          </label>
        )}
        <label className="field-label">
          Email
          <div className="input-wrap">
            <input type="email" value={email} onChange={(event) => setEmail(event.target.value)} required autoComplete="email" />
          </div>
        </label>
        <label className="field-label">
          Password <span>{mode === "signup" ? "10–72 characters" : ""}</span>
          <div className="input-wrap">
            <input
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              required
              minLength={mode === "signup" ? 10 : undefined}
              maxLength={72}
              autoComplete={mode === "signup" ? "new-password" : "current-password"}
            />
          </div>
        </label>
        {error && (
          <div className="form-error" role="alert">
            {error}
          </div>
        )}
        <button className="primary-button full" disabled={busy}>
          {busy ? "Please wait…" : mode === "signup" ? "Create account" : "Log in"}
        </button>
        {health.data?.googleSignIn && (
          <a className="ghost-button full" href={`${API_BASE}/auth/google/start`}>
            Continue with Google
          </a>
        )}
        <div className="divider">or</div>
        <button
          type="button"
          className="ghost-button full"
          onClick={async () => {
            await startGuestSession();
            navigate("/trade/ATHR");
          }}
        >
          Try as guest (24 hours, no sign-up)
        </button>
      </form>
    </div>
  );
}
