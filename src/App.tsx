import { useEffect } from "react";
import { Route, Routes } from "react-router-dom";
import { restoreSession } from "./api/session";
import { Layout } from "./components/Layout";
import { socket } from "./live/socket";
import { Admin } from "./pages/Admin";
import { AuthCallback } from "./pages/AuthCallback";
import { AuthPage } from "./pages/AuthPage";
import { Landing } from "./pages/Landing";
import { Leaderboard } from "./pages/Leaderboard";
import { Markets } from "./pages/Markets";
import { NotFound } from "./pages/NotFound";
import { Portfolio } from "./pages/Portfolio";
import { Trade } from "./pages/Trade";

export default function App() {
  useEffect(() => {
    void restoreSession();
    socket.start();
  }, []);
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route path="/" element={<Landing />} />
        <Route path="/auth" element={<AuthPage />} />
        <Route path="/auth/callback" element={<AuthCallback />} />
        <Route path="/markets" element={<Markets />} />
        <Route path="/trade" element={<Trade />} />
        <Route path="/trade/:symbol" element={<Trade />} />
        <Route path="/portfolio" element={<Portfolio />} />
        <Route path="/leaderboard" element={<Leaderboard />} />
        <Route path="/admin" element={<Admin />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}
