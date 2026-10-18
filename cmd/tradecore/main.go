package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tradecore/internal/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	production := os.Getenv("APP_ENV") == "production"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var s *server
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		st, err := openStore(startCtx, dsn)
		if err == nil {
			s, err = openPersistentServer(startCtx, st)
		}
		cancel()
		if err != nil {
			fatal("could not start with Postgres", "error", err)
		}
	} else if production {
		fatal("DATABASE_URL is required in production")
	} else {
		slog.Warn("DATABASE_URL is unset; state lives in memory and resets on restart")
		s = newServer()
	}
	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		if len(secret) < 32 {
			fatal("JWT_SECRET must be at least 32 bytes")
		}
		s.jwtSecret = []byte(secret)
	} else if production {
		fatal("JWT_SECRET is required in production")
	} else {
		slog.Warn("JWT_SECRET is unset; using an ephemeral development key")
	}
	origins := allowedOrigins(os.Getenv("FRONTEND_ORIGIN"))
	s.hub.originPatterns = websocketOrigins(origins, production)
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
		if port := os.Getenv("PORT"); port != "" {
			addr = ":" + port
		}
	}

	go s.runBots(ctx)
	go s.hub.run(ctx)

	httpServer := &http.Server{
		Addr: addr, Handler: securityHeaders(cors(origins, production, s.routes())),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 1 << 16,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		slog.Info("shutting down")
		// Hijacked WebSocket connections are not tracked by Shutdown.
		s.hub.close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("http shutdown", "error", err)
		}
	}()
	slog.Info("TradeCore listening", "addr", addr, "production", production)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("http server failed", "error", err)
	}
	// Requests have finished; flush whatever the bots queued, then exit.
	<-shutdownDone
	s.closeStore()
	slog.Info("TradeCore stopped cleanly")
}

func fatal(message string, args ...any) {
	slog.Error(message, args...)
	os.Exit(1)
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.health)
	mux.HandleFunc("/api/v1/auth/guest", s.guest)
	mux.HandleFunc("/api/v1/auth/signup", s.signup)
	mux.HandleFunc("/api/v1/auth/login", s.login)
	mux.HandleFunc("/api/v1/auth/refresh", s.refresh)
	mux.HandleFunc("/api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/google/start", s.googleStart)
	mux.HandleFunc("GET /api/v1/auth/google/callback", s.googleCallback)
	mux.HandleFunc("/api/v1/me", s.me)
	mux.HandleFunc("/api/v1/instruments", s.listInstruments)
	mux.HandleFunc("/api/v1/instruments/", s.instrumentRoute)
	mux.HandleFunc("/api/v1/orders", s.ordersRoute)
	mux.HandleFunc("/api/v1/orders/", s.cancelOrder)
	mux.HandleFunc("/api/v1/trades/me", s.myTrades)
	mux.HandleFunc("/api/v1/portfolio", s.portfolio)
	mux.HandleFunc("/api/v1/leaderboard", s.leaderboardHandler)
	mux.HandleFunc("/api/v1/admin/", s.adminRoute)
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Endpoint not found")
	})
	mux.HandleFunc("/ws", s.hub.serveWS)
	mux.Handle("/metrics", s.metrics.handler())
	mux.Handle("/", web.Handler())
	return mux
}

func (s *server) guest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A valid JSON request is required")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "Guest Trader"
	}
	if len(name) > 32 {
		writeError(w, http.StatusBadRequest, "INVALID_NAME", "Display name must be 32 characters or fewer")
		return
	}
	if !s.allowAuthAttempt(r) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many attempts; try again in a minute")
		return
	}
	user := newAccount(s.nextID("user-"), name)
	user.Cash, user.ExpiresAt = balance{Available: startingCash}, time.Now().Add(guestLifetime)
	s.mu.Lock()
	s.accounts[user.ID] = user
	s.markAccount(user.ID)
	s.respondWithSession(w, user.ID, user, http.StatusCreated)
}

// websocketOrigins lists the cross-origin hosts allowed to open a socket;
// same-origin connections are always accepted.
func websocketOrigins(origins map[string]bool, production bool) []string {
	patterns := make([]string, 0, len(origins)+3)
	for origin := range origins {
		if parsed, err := url.Parse(origin); err == nil && parsed.Host != "" {
			patterns = append(patterns, parsed.Host)
		}
	}
	if len(origins) == 0 && !production {
		patterns = append(patterns, "localhost:*", "127.0.0.1:*", "[::1]:*")
	}
	return patterns
}
