package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// dummyHash keeps login timing similar whether or not the email exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("tradecore-timing-equalizer"), 12)

const (
	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 7 * 24 * time.Hour
)

type accessClaims struct {
	jwt.RegisteredClaims
}

func (s *server) issueAccessToken(userID string, accountExpiry time.Time) (string, error) {
	expiresAt := time.Now().Add(accessTokenTTL)
	if !accountExpiry.IsZero() && accountExpiry.Before(expiresAt) {
		expiresAt = accountExpiry
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: userID, IssuedAt: jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	})
	return token.SignedString(s.jwtSecret)
}

// respondWithSession issues a new access and refresh token for the user,
// commits the session together with any other pending changes, and only then
// answers. The caller must hold s.mu; it is released before returning.
func (s *server) respondWithSession(w http.ResponseWriter, userID string, user *account, status int) {
	accessToken, err := s.issueAccessToken(userID, user.ExpiresAt)
	if err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "TOKEN_ERROR", "Could not create session")
		return
	}
	refreshToken, expiresAt := s.startRefreshSession(user)
	if !s.durable(w) {
		return
	}
	s.setRefreshCookie(w, refreshToken, expiresAt)
	writeJSON(w, status, map[string]any{
		"accessToken": accessToken,
		"user": map[string]any{
			"id": userID, "name": user.Name, "isGuest": user.isGuest(), "isAdmin": user.IsAdmin,
		},
	})
}

// startRefreshSession registers a new refresh token for the user. The caller
// must hold s.mu and commit before handing the token out.
func (s *server) startRefreshSession(user *account) (string, time.Time) {
	refreshToken, err := randomToken()
	if err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	expiresAt := time.Now().Add(refreshTokenTTL)
	if !user.ExpiresAt.IsZero() && user.ExpiresAt.Before(expiresAt) {
		expiresAt = user.ExpiresAt
	}
	tokenHash := hashToken(refreshToken)
	s.refreshTokens[tokenHash] = refreshRecord{UserID: user.ID, ExpiresAt: expiresAt}
	s.markToken(tokenHash)
	return refreshToken, expiresAt
}

func (s *server) subjectFromToken(r *http.Request) (string, error) {
	raw, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found {
		return "", errors.New("missing bearer token")
	}
	return s.subjectFromRaw(raw)
}

func (s *server) subjectFromRaw(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("missing token")
	}
	claims := &accessClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %s", token.Method.Alg())
		}
		return s.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return "", errors.New("invalid token")
	}
	return claims.Subject, nil
}

// accountActive reports whether the account exists and has not expired.
func (s *server) accountActive(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.accounts[userID]
	return user != nil && (user.ExpiresAt.IsZero() || time.Now().Before(user.ExpiresAt))
}

func (s *server) authenticatedUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID, err := s.subjectFromToken(r)
	if err != nil || !s.accountActive(userID) {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Session is no longer valid")
		return "", false
	}
	return userID, true
}

func (s *server) allowAuthAttempt(r *http.Request) bool {
	return s.limits.authPerIP.allow(s.clientIP(r), time.Now())
}

func (s *server) signup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A valid JSON request is required")
		return
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	parsedEmail, err := mail.ParseAddress(email)
	if err != nil || parsedEmail.Address != email {
		writeError(w, http.StatusBadRequest, "INVALID_EMAIL", "Enter a valid email address")
		return
	}
	if len(input.Password) < 10 || len(input.Password) > 72 {
		writeError(w, http.StatusBadRequest, "INVALID_PASSWORD", "Password must be between 10 and 72 bytes")
		return
	}
	name := strings.TrimSpace(input.DisplayName)
	if name == "" || len(name) > 32 {
		writeError(w, http.StatusBadRequest, "INVALID_NAME", "Display name must be between 1 and 32 characters")
		return
	}
	if !s.allowAuthAttempt(r) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many attempts; try again in a minute")
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUTH_ERROR", "Could not create account")
		return
	}
	userID := s.nextID("user-")
	user := newAccount(userID, name)
	user.Email, user.PasswordHash, user.Cash = email, string(passwordHash), balance{Available: startingCash}
	user.IsAdmin = s.adminEmails[email]
	s.mu.Lock()
	for _, existing := range s.accounts {
		if existing.Email == email {
			s.mu.Unlock()
			writeError(w, http.StatusConflict, "EMAIL_TAKEN", "An account with this email already exists")
			return
		}
	}
	s.accounts[userID] = user
	s.markAccount(userID)
	s.respondWithSession(w, userID, user, http.StatusCreated)
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A valid JSON request is required")
		return
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if !s.allowAuthAttempt(r) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many attempts; try again in a minute")
		return
	}
	s.mu.Lock()
	var userID string
	passwordHash := dummyHash
	for id, candidate := range s.accounts {
		if email != "" && candidate.Email == email && candidate.PasswordHash != "" {
			userID, passwordHash = id, []byte(candidate.PasswordHash)
			break
		}
	}
	s.mu.Unlock()

	// bcrypt takes ~250 ms at cost 12, so it must run outside the exchange lock.
	passwordOK := bcrypt.CompareHashAndPassword(passwordHash, []byte(input.Password)) == nil
	s.mu.Lock()
	user := s.accounts[userID]
	if userID == "" || !passwordOK || user == nil {
		s.mu.Unlock()
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email or password is incorrect")
		return
	}
	if isAdmin := s.adminEmails[user.Email]; isAdmin != user.IsAdmin {
		user.IsAdmin = isAdmin
		s.markAccount(userID)
	}
	s.respondWithSession(w, userID, user, http.StatusOK)
}

func (s *server) refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	cookie, err := r.Cookie("tradecore_refresh")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Refresh session is missing")
		return
	}
	s.mu.Lock()
	tokenHash := hashToken(cookie.Value)
	record, exists := s.refreshTokens[tokenHash]
	user := s.accounts[record.UserID]
	if exists {
		// Refresh tokens are single-use: rotate on every refresh.
		delete(s.refreshTokens, tokenHash)
		s.markToken(tokenHash)
	}
	if !exists || user == nil || time.Now().After(record.ExpiresAt) ||
		(!user.ExpiresAt.IsZero() && time.Now().After(user.ExpiresAt)) {
		if !s.durable(w) {
			return
		}
		s.clearRefreshCookie(w)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Refresh session is no longer valid")
		return
	}
	s.respondWithSession(w, record.UserID, user, http.StatusOK)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	// Logout only needs the refresh cookie, so it still works after the
	// short-lived access token has expired.
	s.mu.Lock()
	if cookie, err := r.Cookie("tradecore_refresh"); err == nil {
		tokenHash := hashToken(cookie.Value)
		if _, exists := s.refreshTokens[tokenHash]; exists {
			delete(s.refreshTokens, tokenHash)
			s.markToken(tokenHash)
		}
	}
	if !s.durable(w) {
		return
	}
	s.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) setRefreshCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: "tradecore_refresh", Value: token, Path: "/api/v1/auth",
		Expires: expiresAt, MaxAge: int(time.Until(expiresAt).Seconds()),
		HttpOnly: true, Secure: s.secureCookies, SameSite: s.cookieSameSite(),
	})
}

func (s *server) cookieSameSite() http.SameSite {
	if s.crossSiteCookies {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

func (s *server) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: "tradecore_refresh", Value: "", Path: "/api/v1/auth",
		MaxAge: -1, HttpOnly: true, Secure: s.secureCookies,
		SameSite: s.cookieSameSite(),
	})
}

func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
