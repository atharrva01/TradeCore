import { useEffect } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router-dom";
import { renewSession } from "../api/session";

/** Google sign-in lands here with a refresh cookie; exchange it for a session. */
export function AuthCallback() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const failed = params.has("error");
  useEffect(() => {
    if (failed) return;
    renewSession().then((token) => navigate(token ? "/trade/ATHR" : "/auth?error=google", { replace: true }));
  }, [failed, navigate]);
  if (failed) return <Navigate to="/auth?error=google" replace />;
  return <div className="page-loading">Signing you in…</div>;
}
