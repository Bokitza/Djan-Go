package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yoav/djan-go/auth"
	"github.com/yoav/djan-go/internal/testdb"
	"github.com/yoav/djan-go/query"
)

func TestPasswordHashing(t *testing.T) {
	hashed, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hashed, "argon2id$v=19$") || !auth.CheckPassword("correct horse", hashed) || auth.CheckPassword("wrong", hashed) {
		t.Fatalf("argon2id round trip failed: %s", hashed)
	}
	if auth.CheckPassword("x", "!") || auth.CheckPassword("x", "argon2id$garbage") {
		t.Fatal("malformed hashes must not verify")
	}
}

func TestSessionsAndPermissions(t *testing.T) {
	ctx, _ := testdb.New(t)
	testdb.CreateTables(t, ctx)
	if err := auth.CreatePermissions(ctx); err != nil {
		t.Fatal(err)
	}

	alice, err := auth.CreateUser(ctx, "alice", "alice@example.com", "s3cret!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(ctx, "alice", "nope"); err != auth.ErrInvalidCredentials {
		t.Fatalf("bad password: %v", err)
	}
	if _, err := auth.Authenticate(ctx, "nobody", "s3cret!"); err != auth.ErrInvalidCredentials {
		t.Fatalf("unknown user: %v", err)
	}
	authenticated, err := auth.Authenticate(ctx, "alice", "s3cret!")
	if err != nil || authenticated.ID != alice.ID {
		t.Fatalf("authenticate: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/login", nil).WithContext(ctx)
	if err := auth.Login(ctx, recorder, request, authenticated); err != nil {
		t.Fatal(err)
	}
	cookie := recorder.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure {
		t.Fatalf("cookie flags: %+v", cookie)
	}
	stored, err := query.Objects[auth.Session]().First(ctx)
	if err != nil || stored.Key == cookie.Value || len(stored.Key) != 64 {
		t.Fatalf("session key must be stored hashed: %+v %v", stored, err)
	}

	protected := auth.Middleware(auth.RequirePerm("auth.view_group", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(auth.CurrentUser(r).Username))
	})))
	serve := func(withCookie bool) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		request := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
		if withCookie {
			request.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		}
		protected.ServeHTTP(response, request)
		return response
	}
	if code := serve(false).Code; code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code := serve(true).Code; code != http.StatusForbidden {
		t.Fatalf("missing permission: %d", code)
	}

	editors := &auth.Group{Name: "editors"}
	if err := query.Create(ctx, editors); err != nil {
		t.Fatal(err)
	}
	if err := auth.GrantGroupPermission(ctx, editors, "auth.view_group"); err != nil {
		t.Fatal(err)
	}
	if err := auth.AddToGroup(ctx, alice, editors); err != nil {
		t.Fatal(err)
	}
	if response := serve(true); response.Code != http.StatusOK || response.Body.String() != "alice" {
		t.Fatalf("group permission: %d %s", response.Code, response.Body)
	}
	if err := auth.GrantPermission(ctx, alice, "auth.delete_user"); err != nil {
		t.Fatal(err)
	}
	if !alice.HasPerm(ctx, "auth.delete_user") || alice.HasPerm(ctx, "auth.add_user") {
		t.Fatal("direct permission check")
	}

	if _, err := query.Objects[auth.Session]().Update(ctx, func(s *auth.Session) []query.Assign {
		return []query.Assign{query.Set(&s.ExpiresAt, time.Now().Add(-time.Minute))}
	}); err != nil {
		t.Fatal(err)
	}
	if code := serve(true).Code; code != http.StatusUnauthorized {
		t.Fatalf("expired session must be rejected: %d", code)
	}
	if deleted, err := auth.DeleteExpiredSessions(ctx); err != nil || deleted != 1 {
		t.Fatalf("cleanup: %d %v", deleted, err)
	}
}
