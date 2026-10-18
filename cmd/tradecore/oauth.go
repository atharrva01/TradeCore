package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// googleOAuth implements the authorization-code flow with Google. It is
// enabled when GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET and PUBLIC_URL are set.
type googleOAuth struct {
	clientID     string
	clientSecret string
	redirectURL  string
	afterLogin   string
	authURL      string
	tokenURL     string
	userinfoURL  string
	client       *http.Client
}

const oauthStateCookie = "tradecore_oauth_state"

func googleOAuthFromEnv() *googleOAuth {
	id, secret, public := os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET"), strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")
	if id == "" || secret == "" || public == "" {
		return nil
	}
	frontend := strings.TrimRight(os.Getenv("FRONTEND_URL"), "/")
	if frontend == "" {
		frontend = public
	}
	return &googleOAuth{
		clientID: id, clientSecret: secret,
		redirectURL: public + "/api/v1/auth/google/callback",
		afterLogin:  frontend + "/auth/callback",
		authURL:     "https://accounts.google.com/o/oauth2/v2/auth",
		tokenURL:    "https://oauth2.googleapis.com/token",
		userinfoURL: "https://openidconnect.googleapis.com/v1/userinfo",
		client:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *server) googleStart(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		writeError(w, http.StatusNotFound, "OAUTH_DISABLED", "Google sign-in is not configured")
		return
	}
	state, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OAUTH_ERROR", "Could not start Google sign-in")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oauthStateCookie, Value: state, Path: "/api/v1/auth/google", MaxAge: 600,
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode,
	})
	query := url.Values{
		"client_id": {s.google.clientID}, "redirect_uri": {s.google.redirectURL}, "response_type": {"code"},
		"scope": {"openid email profile"}, "state": {state}, "prompt": {"select_account"},
	}
	http.Redirect(w, r, s.google.authURL+"?"+query.Encode(), http.StatusFound)
}

type googleProfile struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

func (s *server) googleCallback(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		writeError(w, http.StatusNotFound, "OAUTH_DISABLED", "Google sign-in is not configured")
		return
	}
	fail := func(reason string) {
		slog.Warn("google sign-in failed", "reason", reason)
		http.Redirect(w, r, s.google.afterLogin+"?error=google", http.StatusFound)
	}
	cookie, err := r.Cookie(oauthStateCookie)
	if err != nil || cookie.Value == "" || cookie.Value != r.URL.Query().Get("state") {
		fail("state mismatch")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Path: "/api/v1/auth/google", MaxAge: -1})
	profile, err := s.google.exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		fail(err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(profile.Email))
	if email == "" || !profile.EmailVerified {
		fail("email not verified")
		return
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = strings.Split(email, "@")[0]
	}
	if len(name) > 32 {
		name = name[:32]
	}

	s.mu.Lock()
	var user *account
	for _, candidate := range s.accounts {
		if candidate.Email == email {
			user = candidate
			break
		}
	}
	if user == nil {
		user = newAccount(s.nextID("user-"), name)
		user.Email, user.Cash = email, balance{Available: startingCash}
		s.accounts[user.ID] = user
	}
	user.IsAdmin = s.adminEmails[email]
	s.markAccount(user.ID)
	refreshToken, expiresAt := s.startRefreshSession(user)
	if err := s.commitAndUnlock(); err != nil {
		fail("could not save session")
		return
	}
	s.setRefreshCookie(w, refreshToken, expiresAt)
	http.Redirect(w, r, s.google.afterLogin, http.StatusFound)
}

func (g *googleOAuth) exchange(ctx context.Context, code string) (googleProfile, error) {
	if code == "" {
		return googleProfile{}, fmt.Errorf("missing code")
	}
	form := url.Values{
		"code": {code}, "client_id": {g.clientID}, "client_secret": {g.clientSecret},
		"redirect_uri": {g.redirectURL}, "grant_type": {"authorization_code"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return googleProfile{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := g.client.Do(request)
	if err != nil {
		return googleProfile{}, fmt.Errorf("token exchange: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&token) != nil || token.AccessToken == "" {
		return googleProfile{}, fmt.Errorf("token exchange returned %d", response.StatusCode)
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, g.userinfoURL, nil)
	if err != nil {
		return googleProfile{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err = g.client.Do(request)
	if err != nil {
		return googleProfile{}, fmt.Errorf("userinfo: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var profile googleProfile
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&profile) != nil {
		return googleProfile{}, fmt.Errorf("userinfo returned %d", response.StatusCode)
	}
	return profile, nil
}
