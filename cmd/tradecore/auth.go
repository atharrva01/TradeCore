package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

func (s *server) createSession(w http.ResponseWriter, userID string, user *account, status int) {
	accessToken, err := s.issueAccessToken(userID, user.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_ERROR", "Could not create session")
		return
	}
	refreshToken, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_ERROR", "Could not create session")
		return
	}
	expiresAt := time.Now().Add(refreshTokenTTL)
	if !user.ExpiresAt.IsZero() && user.ExpiresAt.Before(expiresAt) {
		expiresAt = user.ExpiresAt
	}
	s.refreshTokens[hashToken(refreshToken)] = refreshRecord{UserID: userID, ExpiresAt: expiresAt}
	s.setRefreshCookie(w, refreshToken, expiresAt)
	writeJSON(w, status, map[string]any{
		"accessToken": accessToken,
		"user":        map[string]string{"id": userID, "name": user.Name},
	})
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
	s.mu.Lock()
	allowed := s.allowAuthAttempt(r, time.Now())
	s.mu.Unlock()
	if !allowed {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many attempts; try again in a minute")
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUTH_ERROR", "Could not create account")
		return
	}
	userID := s.nextID("user-")
	user := &account{
		Name: name, Email: email, PasswordHash: string(passwordHash),
		Cash: balance{Available: startingCash}, Shares: make(map[string]balance),
		Positions: make(map[string]*position),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.accounts {
		if existing.Email == email {
			writeError(w, http.StatusConflict, "EMAIL_TAKEN", "An account with this email already exists")
			return
		}
	}
	s.accounts[userID] = user
	s.createSession(w, userID, user, http.StatusCreated)
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
	s.mu.Lock()
	if !s.allowAuthAttempt(r, time.Now()) {
		s.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many attempts; try again in a minute")
		return
	}
	var userID string
	passwordHash := dummyHash
	for id, candidate := range s.accounts {
		if email != "" && candidate.Email == email {
			userID, passwordHash = id, []byte(candidate.PasswordHash)
			break
		}
	}
	s.mu.Unlock()

	// bcrypt takes ~250 ms at cost 12, so it must run outside the exchange lock.
	passwordOK := bcrypt.CompareHashAndPassword(passwordHash, []byte(input.Password)) == nil
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.accounts[userID]
	if userID == "" || !passwordOK || user == nil {
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email or password is incorrect")
		return
	}
	s.createSession(w, userID, user, http.StatusOK)
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
	defer s.mu.Unlock()
	tokenHash := hashToken(cookie.Value)
	record, exists := s.refreshTokens[tokenHash]
	user := s.accounts[record.UserID]
	if !exists || user == nil || time.Now().After(record.ExpiresAt) ||
		(!user.ExpiresAt.IsZero() && time.Now().After(user.ExpiresAt)) {
		delete(s.refreshTokens, tokenHash)
		s.clearRefreshCookie(w)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Refresh session is no longer valid")
		return
	}
	delete(s.refreshTokens, tokenHash)
	s.createSession(w, record.UserID, user, http.StatusOK)
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
		delete(s.refreshTokens, hashToken(cookie.Value))
	}
	s.mu.Unlock()
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
