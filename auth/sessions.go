package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Bokitza/djan-go/models"
	"github.com/Bokitza/djan-go/query"
)

// SessionSettings configures the session cookie.
type SessionSettings struct {
	CookieName string
	MaxAge     time.Duration
	// AlwaysSecure forces the Secure cookie flag. Otherwise it is set for HTTPS requests
	// (directly or behind a proxy sending X-Forwarded-Proto: https).
	AlwaysSecure bool
}

// Settings holds the session configuration.
var Settings = SessionSettings{CookieName: "djan_session", MaxAge: 14 * 24 * time.Hour}

// ErrInvalidCredentials is returned by Authenticate.
var ErrInvalidCredentials = errors.New("auth: invalid username or password")

type userKey struct{}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func isSecure(r *http.Request) bool {
	return Settings.AlwaysSecure || r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge time.Duration) {
	cookie := &http.Cookie{
		Name:     Settings.CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(maxAge.Seconds()),
	}
	if maxAge < 0 {
		cookie.MaxAge = -1
	}
	http.SetCookie(w, cookie)
}

// Authenticate checks credentials and returns the active user.
func Authenticate(ctx context.Context, username, password string) (*User, error) {
	user, err := query.Objects[User]().Get(ctx, func(u *User) query.Cond { return query.Eq(&u.Username, username) })
	if errors.Is(err, query.ErrNotFound) {
		CheckPassword(password, dummyHash) // keep timing similar for unknown users
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if !user.CheckPassword(password) || !user.IsActive {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

var dummyHash, _ = HashPassword("djan-go timing equalizer")

// Login starts a new session for user, replacing any session sent with the request.
func Login(ctx context.Context, w http.ResponseWriter, r *http.Request, user *User) error {
	deleteRequestSession(ctx, r)
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	key := base64.RawURLEncoding.EncodeToString(random)
	session := &Session{Key: hashKey(key), User: models.RefTo(user), ExpiresAt: time.Now().Add(Settings.MaxAge)}
	if err := query.Create(ctx, session); err != nil {
		return err
	}
	now := time.Now()
	user.LastLogin = &now
	if _, err := query.Objects[User]().Where(func(u *User) query.Cond { return query.Eq(&u.ID, user.ID) }).
		Update(ctx, func(u *User) []query.Assign { return []query.Assign{query.Set(&u.LastLogin, &now)} }); err != nil {
		return err
	}
	setCookie(w, r, key, Settings.MaxAge)
	return nil
}

func deleteRequestSession(ctx context.Context, r *http.Request) {
	cookie, err := r.Cookie(Settings.CookieName)
	if err != nil || cookie.Value == "" {
		return
	}
	hashed := hashKey(cookie.Value)
	_, _ = query.Objects[Session]().Where(func(s *Session) query.Cond { return query.Eq(&s.Key, hashed) }).Delete(ctx)
}

// Logout ends the request's session and clears the cookie.
func Logout(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	deleteRequestSession(ctx, r)
	setCookie(w, r, "", -1)
}

func loadUser(w http.ResponseWriter, r *http.Request) *User {
	cookie, err := r.Cookie(Settings.CookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	ctx := r.Context()
	hashed := hashKey(cookie.Value)
	now := time.Now()
	session, err := query.Objects[Session]().Get(ctx, func(s *Session) query.Cond {
		return query.And(query.Eq(&s.Key, hashed), query.Gt(&s.ExpiresAt, now))
	})
	if err != nil {
		return nil
	}
	user, err := session.User.Get(ctx)
	if err != nil || user == nil || !user.IsActive {
		return nil
	}
	if session.ExpiresAt.Sub(now) < Settings.MaxAge/2 {
		expiresAt := now.Add(Settings.MaxAge)
		_, err := query.Objects[Session]().Where(func(s *Session) query.Cond { return query.Eq(&s.ID, session.ID) }).
			Update(ctx, func(s *Session) []query.Assign { return []query.Assign{query.Set(&s.ExpiresAt, expiresAt)} })
		if err == nil {
			setCookie(w, r, cookie.Value, Settings.MaxAge)
		}
	}
	return user
}

type sessionLoadedKey struct{}

// Middleware loads the session user (if any) into the request context. Nesting it is harmless:
// the session is only looked up once per request.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(sessionLoadedKey{}) == nil {
			ctx := context.WithValue(r.Context(), sessionLoadedKey{}, true)
			if user := loadUser(w, r); user != nil {
				ctx = WithUser(ctx, user)
			}
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// WithUser stores a user in a context.
func WithUser(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

// UserFromContext returns the logged in user, or nil.
func UserFromContext(ctx context.Context) *User {
	user, _ := ctx.Value(userKey{}).(*User)
	return user
}

// CurrentUser returns the logged in user of the request, or nil.
func CurrentUser(r *http.Request) *User { return UserFromContext(r.Context()) }

// RequireLogin responds 401 to anonymous requests.
func RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if CurrentUser(r) == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePerm responds 401 to anonymous requests and 403 when the user lacks the permission.
func RequirePerm(permission string, next http.Handler) http.Handler {
	return RequireLogin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CurrentUser(r).HasPerm(r.Context(), permission) {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// DeleteExpiredSessions removes expired sessions and returns how many were deleted.
func DeleteExpiredSessions(ctx context.Context) (int64, error) {
	now := time.Now()
	return query.Objects[Session]().Where(func(s *Session) query.Cond { return query.Lte(&s.ExpiresAt, now) }).Delete(ctx)
}
