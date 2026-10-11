package admin_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yoav-rogat/djan-go/admin"
	"github.com/yoav-rogat/djan-go/auth"
	"github.com/yoav-rogat/djan-go/db"
	"github.com/yoav-rogat/djan-go/importexport"
	"github.com/yoav-rogat/djan-go/testdb"
	"github.com/yoav-rogat/djan-go/models"
	"github.com/yoav-rogat/djan-go/query"
)

type Ticket struct {
	models.Model
	Title  string
	Owner  models.FK[auth.User]
	Status string
	Secret bool
}

func (t *Ticket) Schema(s *models.Schema) {
	s.App("helpdesk")
	s.Ordering(models.Asc(&t.Title))
	s.Char(&t.Title, 100)
	models.ForeignKey(s, &t.Owner)
	s.Char(&t.Status, 10).Choices("open", "closed").Default("open")
	s.Bool(&t.Secret).Default(false)
}

func (t *Ticket) String() string { return t.Title }

func init() { models.Register[Ticket]() }

func ownedBy(t *Ticket, user *auth.User) query.Cond { return query.Eq(&t.Owner.ID, user.ID) }

func newSite() *admin.Site {
	site := admin.NewSite("Helpdesk")
	admin.Register(site, admin.ModelAdmin[Ticket]{
		Configure: func(t *Ticket, config *admin.Config) {
			config.ListDisplay(admin.F(&t.Title), admin.F(&t.Owner), admin.F(&t.Status))
			config.SearchFields(&t.Title)
			config.ListFilter(admin.F(&t.Status))
		},
		ViewQuerySet: func(r *admin.Request, tickets query.QuerySet[Ticket]) query.QuerySet[Ticket] {
			if r.User.IsSuperuser {
				return tickets
			}
			return tickets.Where(func(t *Ticket) query.Cond {
				return query.Or(query.Eq(&t.Secret, false), ownedBy(t, r.User))
			})
		},
		ChangeQuerySet: func(r *admin.Request, tickets query.QuerySet[Ticket]) query.QuerySet[Ticket] {
			return tickets.Where(func(t *Ticket) query.Cond { return ownedBy(t, r.User) })
		},
		DeleteQuerySet: func(r *admin.Request, tickets query.QuerySet[Ticket]) query.QuerySet[Ticket] {
			return tickets.Where(func(t *Ticket) query.Cond {
				return query.And(ownedBy(t, r.User), query.Eq(&t.Status, "closed"))
			})
		},
		CanAdd: func(r *admin.Request) bool { return r.User.Username != "noadd" },
		ImportExport: &admin.ImportExport[Ticket]{
			Import: true, Export: true, ExportSelected: true,
			Resources: []importexport.Resource[Ticket]{ticketResource},
		},
	})
	return site
}

type fixture struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	server *httptest.Server
	alice  *auth.User
	bob    *auth.User
	// tickets by title
	tickets map[string]*Ticket
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx, pool := testdb.New(t)
	testdb.CreateTables(t, ctx)
	if err := auth.CreatePermissions(ctx); err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: ctx, pool: pool, tickets: map[string]*Ticket{}}
	f.alice = createStaff(t, ctx, "alice")
	f.bob = createStaff(t, ctx, "bob")
	for _, permission := range []string{"helpdesk.view_ticket", "helpdesk.add_ticket", "helpdesk.change_ticket", "helpdesk.delete_ticket"} {
		for _, user := range []*auth.User{f.alice, f.bob} {
			if err := auth.GrantPermission(ctx, user, permission); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, ticket := range []*Ticket{
		{Title: "alice open", Owner: models.RefTo(f.alice), Status: "open"},
		{Title: "alice secret", Owner: models.RefTo(f.alice), Status: "closed", Secret: true},
		{Title: "bob open", Owner: models.RefTo(f.bob), Status: "open"},
		{Title: "bob closed", Owner: models.RefTo(f.bob), Status: "closed"},
		{Title: "bob closed too", Owner: models.RefTo(f.bob), Status: "closed"},
	} {
		if err := query.Create(ctx, ticket); err != nil {
			t.Fatal(err)
		}
		f.tickets[ticket.Title] = ticket
	}
	f.server = httptest.NewServer(withPool(pool, newSite().Handler()))
	t.Cleanup(f.server.Close)
	return f
}

func withPool(pool *pgxpool.Pool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(db.WithPool(r.Context(), pool)))
	})
}

func createStaff(t *testing.T, ctx context.Context, username string) *auth.User {
	t.Helper()
	user, err := auth.CreateUser(ctx, username, username+"@example.com", "password-"+username)
	if err != nil {
		t.Fatal(err)
	}
	user.IsStaff = true
	if err := query.Save(ctx, user); err != nil {
		t.Fatal(err)
	}
	return user
}

type client struct {
	t      *testing.T
	base   string
	client *http.Client
}

func (f *fixture) anonymous(t *testing.T) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: f.server.URL, client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (f *fixture) login(t *testing.T, username string) *client {
	c := f.anonymous(t)
	response, _ := c.post("/admin/login/", url.Values{"username": {username}, "password": {"password-" + username}, "next": {"/admin/"}}, nil)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("login %s: status %d", username, response.StatusCode)
	}
	return c
}

func (c *client) do(request *http.Request) (*http.Response, string) {
	c.t.Helper()
	response, err := c.client.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response, string(body)
}

func (c *client) get(path string, headers map[string]string) (*http.Response, string) {
	request, _ := http.NewRequest(http.MethodGet, c.base+path, nil)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return c.do(request)
}

func (c *client) post(path string, form url.Values, headers map[string]string) (*http.Response, string) {
	request, _ := http.NewRequest(http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return c.do(request)
}

func ticketURL(ticket *Ticket) string {
	return "/admin/helpdesk/ticket/" + strconv.FormatInt(ticket.ID, 10) + "/"
}

func (f *fixture) reload(t *testing.T, ticket *Ticket) (*Ticket, bool) {
	t.Helper()
	fresh, err := query.Objects[Ticket]().GetByPK(f.ctx, ticket.ID)
	if err == query.ErrNotFound {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return fresh, true
}

func TestLogin(t *testing.T) {
	f := setup(t)
	anonymous := f.anonymous(t)

	response, _ := anonymous.get("/admin/helpdesk/ticket/", nil)
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/admin/login/?next=%2Fadmin%2Fhelpdesk%2Fticket%2F" {
		t.Fatalf("anonymous list: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	response, body := anonymous.post("/admin/login/", url.Values{"username": {"alice"}, "password": {"wrong"}}, nil)
	if response.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "correct username and password") {
		t.Fatalf("wrong password: %d", response.StatusCode)
	}

	visitor, err := auth.CreateUser(f.ctx, "visitor", "", "password-visitor")
	if err != nil {
		t.Fatal(err)
	}
	response, _ = anonymous.post("/admin/login/", url.Values{"username": {visitor.Username}, "password": {"password-visitor"}}, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("non-staff login: %d", response.StatusCode)
	}

	response, _ = anonymous.post("/admin/login/", url.Values{"username": {"alice"}, "password": {"password-alice"}, "next": {"https://evil.example/"}}, nil)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/admin/" {
		t.Fatalf("login must ignore foreign next: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	response, body = anonymous.get("/admin/", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Tickets") {
		t.Fatalf("dashboard after login: %d", response.StatusCode)
	}

	response, _ = anonymous.post("/admin/logout/", nil, nil)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", response.StatusCode)
	}
	if response, _ = anonymous.get("/admin/", nil); response.StatusCode != http.StatusFound {
		t.Fatalf("dashboard after logout: %d", response.StatusCode)
	}
}

func TestModelPermissionRequired(t *testing.T) {
	f := setup(t)
	createStaff(t, f.ctx, "carol")
	carol := f.login(t, "carol")
	response, body := carol.get("/admin/", nil)
	if response.StatusCode != http.StatusOK || strings.Contains(body, "/admin/helpdesk/ticket/") {
		t.Fatalf("dashboard must hide models without permission")
	}
	if response, _ := carol.get("/admin/helpdesk/ticket/", nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("list without view permission: %d", response.StatusCode)
	}
	if response, _ := carol.get(ticketURL(f.tickets["bob open"]), nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("detail without view permission: %d", response.StatusCode)
	}
	if response, _ := carol.get("/admin/auth/user/", nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("users without permission: %d", response.StatusCode)
	}
}

func TestViewRule(t *testing.T) {
	f := setup(t)
	bob := f.login(t, "bob")

	response, body := bob.get("/admin/helpdesk/ticket/", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", response.StatusCode)
	}
	for _, title := range []string{"alice open", "bob open", "bob closed"} {
		if !strings.Contains(body, title) {
			t.Errorf("list should show %q", title)
		}
	}
	if strings.Contains(body, "alice secret") {
		t.Error("list must hide alice's secret ticket")
	}

	if response, _ := bob.get(ticketURL(f.tickets["alice secret"]), nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden ticket detail: %d", response.StatusCode)
	}
	_, body = bob.get("/admin/helpdesk/ticket/autocomplete/?q=alice", nil)
	if !strings.Contains(body, "alice open") || strings.Contains(body, "alice secret") {
		t.Fatalf("autocomplete must apply the view rule: %s", body)
	}

	_, body = f.login(t, "alice").get("/admin/helpdesk/ticket/", nil)
	if !strings.Contains(body, "alice secret") {
		t.Fatal("owners see their secret tickets")
	}
}

func TestListSearchFilterAndPartial(t *testing.T) {
	f := setup(t)
	bob := f.login(t, "bob")
	_, body := bob.get("/admin/helpdesk/ticket/?q=closed+too", nil)
	if !strings.Contains(body, "bob closed too") || strings.Contains(body, "alice open") {
		t.Fatal("search")
	}
	_, body = bob.get("/admin/helpdesk/ticket/?f.status=open", nil)
	if !strings.Contains(body, "alice open") || strings.Contains(body, "bob closed") {
		t.Fatal("filter")
	}
	response, body := bob.get("/admin/helpdesk/ticket/?o=-title", map[string]string{"HX-Request": "true", "HX-Target": "results"})
	if response.StatusCode != http.StatusOK || strings.Contains(body, "<html") || !strings.Contains(body, `id="results"`) {
		t.Fatal("HTMX requests for #results get the partial only")
	}
	if strings.Index(body, "bob open") > strings.Index(body, "alice open") {
		t.Fatal("descending title order")
	}
}

func TestChangeRule(t *testing.T) {
	f := setup(t)
	bob := f.login(t, "bob")
	aliceOpen := f.tickets["alice open"]

	_, body := bob.get(ticketURL(aliceOpen), nil)
	if !strings.Contains(body, "Read only") || strings.Contains(body, `name="title"`) {
		t.Fatal("visible but unchangeable tickets render read-only")
	}
	form := url.Values{"title": {"hijacked"}, "owner": {strconv.FormatInt(f.bob.ID, 10)}, "status": {"open"}}
	if response, _ := bob.post(ticketURL(aliceOpen), form, nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("changing another user's ticket: %d", response.StatusCode)
	}
	if fresh, _ := f.reload(t, aliceOpen); fresh.Title != "alice open" {
		t.Fatal("ticket was modified")
	}

	bobOpen := f.tickets["bob open"]
	form = url.Values{"title": {"bob renamed"}, "owner": {strconv.FormatInt(f.bob.ID, 10)}, "status": {"open"}, "_action": {"continue"}}
	response, _ := bob.post(ticketURL(bobOpen), form, nil)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != ticketURL(bobOpen) {
		t.Fatalf("own ticket save: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	if fresh, _ := f.reload(t, bobOpen); fresh.Title != "bob renamed" {
		t.Fatal("ticket was not saved")
	}
	_, body = bob.get(ticketURL(bobOpen), nil)
	if !strings.Contains(body, "was changed successfully") {
		t.Fatal("success flash")
	}

	response, _ = bob.post(ticketURL(bobOpen), url.Values{"title": {"x"}, "owner": {strconv.FormatInt(f.bob.ID, 10)}, "status": {"open"}},
		map[string]string{"HX-Request": "true"})
	if response.StatusCode != http.StatusOK || response.Header.Get("HX-Redirect") != "/admin/helpdesk/ticket/" {
		t.Fatalf("HTMX save redirects with HX-Redirect: %d %q", response.StatusCode, response.Header.Get("HX-Redirect"))
	}
}

func TestFormValidation(t *testing.T) {
	f := setup(t)
	bob := f.login(t, "bob")
	bobOpen := f.tickets["bob open"]
	response, body := bob.post(ticketURL(bobOpen), url.Values{"title": {""}, "owner": {strconv.FormatInt(f.bob.ID, 10)}, "status": {"pending"}}, nil)
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "This field is required") || !strings.Contains(body, "Select a valid choice") {
		t.Fatalf("validation errors: %d", response.StatusCode)
	}
	// Bob can't see users, so he can't point a ticket at another user.
	response, body = bob.post(ticketURL(bobOpen), url.Values{"title": {"x"}, "owner": {strconv.FormatInt(f.alice.ID, 10)}, "status": {"open"}}, nil)
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "not one of the available choices") {
		t.Fatalf("foreign key tampering: %d", response.StatusCode)
	}
	if fresh, _ := f.reload(t, bobOpen); fresh.Owner.ID != f.bob.ID || fresh.Title != "bob open" {
		t.Fatal("invalid form must not save")
	}
}

func TestAdd(t *testing.T) {
	f := setup(t)
	// Foreign keys may only point at rows the user can see in the admin.
	if err := auth.GrantPermission(f.ctx, f.bob, "auth.view_user"); err != nil {
		t.Fatal(err)
	}
	bob := f.login(t, "bob")
	response, _ := bob.post("/admin/helpdesk/ticket/add/", url.Values{"title": {"new"}, "owner": {strconv.FormatInt(f.bob.ID, 10)}, "status": {"open"}}, nil)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("add: %d", response.StatusCode)
	}
	if exists, _ := query.Objects[Ticket]().Where(func(t *Ticket) query.Cond { return query.Eq(&t.Title, "new") }).Exists(f.ctx); !exists {
		t.Fatal("ticket not created")
	}

	noAdd := createStaff(t, f.ctx, "noadd")
	if err := auth.GrantPermission(f.ctx, noAdd, "helpdesk.add_ticket"); err != nil {
		t.Fatal(err)
	}
	if err := auth.GrantPermission(f.ctx, noAdd, "helpdesk.view_ticket"); err != nil {
		t.Fatal(err)
	}
	if response, _ := f.login(t, "noadd").get("/admin/helpdesk/ticket/add/", nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("CanAdd=false: %d", response.StatusCode)
	}
}

func TestDeleteRule(t *testing.T) {
	f := setup(t)
	bob := f.login(t, "bob")

	if response, _ := bob.post(ticketURL(f.tickets["bob open"])+"delete/", nil, nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("deleting an open ticket: %d", response.StatusCode)
	}
	if response, _ := bob.post(ticketURL(f.tickets["alice secret"])+"delete/", nil, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleting a hidden ticket: %d", response.StatusCode)
	}
	if response, _ := bob.post(ticketURL(f.tickets["bob closed"])+"delete/", nil, nil); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("deleting own closed ticket: %d", response.StatusCode)
	}
	if _, exists := f.reload(t, f.tickets["bob closed"]); exists {
		t.Fatal("ticket not deleted")
	}
	for _, title := range []string{"bob open", "alice secret"} {
		if _, exists := f.reload(t, f.tickets[title]); !exists {
			t.Fatalf("%s must survive", title)
		}
	}
}

func TestBulkDelete(t *testing.T) {
	f := setup(t)
	bob := f.login(t, "bob")
	var ids []string
	for _, ticket := range f.tickets {
		ids = append(ids, strconv.FormatInt(ticket.ID, 10))
	}
	response, _ := bob.post("/admin/helpdesk/ticket/bulk-delete/?f.status=closed", url.Values{"ids": ids}, nil)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/admin/helpdesk/ticket/?f.status=closed" {
		t.Fatalf("bulk delete: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	count, err := query.Objects[Ticket]().Count(f.ctx)
	if err != nil || count != 3 {
		t.Fatalf("only bob's two closed tickets may be deleted, %d left (%v)", count, err)
	}
	_, body := bob.get("/admin/helpdesk/ticket/", nil)
	if !strings.Contains(body, "Deleted 2 tickets.") || !strings.Contains(body, "3 could not be deleted") {
		t.Fatal("bulk delete flash")
	}
}

func TestSuperuserSeesEverything(t *testing.T) {
	f := setup(t)
	root, err := auth.CreateSuperuser(f.ctx, "root", "", "password-root")
	if err != nil {
		t.Fatal(err)
	}
	_ = root
	_, body := f.login(t, "root").get("/admin/helpdesk/ticket/", nil)
	if !strings.Contains(body, "alice secret") {
		t.Fatal("superusers bypass the view rule here")
	}
	if response, _ := f.login(t, "root").get("/admin/auth/user/", nil); response.StatusCode != http.StatusOK {
		t.Fatalf("superuser users list: %d", response.StatusCode)
	}
}

func TestUserAdmin(t *testing.T) {
	f := setup(t)
	if _, err := auth.CreateSuperuser(f.ctx, "root", "", "password-root"); err != nil {
		t.Fatal(err)
	}
	editors := &auth.Group{Name: "Editors"}
	if err := query.Create(f.ctx, editors); err != nil {
		t.Fatal(err)
	}
	root := f.login(t, "root")
	userURL := "/admin/auth/user/" + strconv.FormatInt(f.bob.ID, 10) + "/"
	form := url.Values{
		"username": {"bob"}, "email": {"bob@example.com"}, "is_active": {"true"}, "is_staff": {"true"},
		"new_password": {"brand-new-password"}, "new_password_confirm": {"brand-new-password"},
		"groups": {strconv.FormatInt(editors.ID, 10)},
	}
	if response, body := root.post(userURL, form, nil); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("superuser edits bob: %d\n%s", response.StatusCode, body)
	}
	if _, err := auth.Authenticate(f.ctx, "bob", "brand-new-password"); err != nil {
		t.Fatalf("password not changed: %v", err)
	}
	if count, _ := query.Objects[auth.UserGroup]().Count(f.ctx); count != 1 {
		t.Fatalf("group membership not saved: %d", count)
	}

	form.Set("new_password_confirm", "different")
	if response, body := root.post(userURL, form, nil); response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "didn&#39;t match") {
		t.Fatalf("password confirmation: %d", response.StatusCode)
	}

	for _, permission := range []string{"auth.view_user", "auth.change_user"} {
		if err := auth.GrantPermission(f.ctx, f.alice, permission); err != nil {
			t.Fatal(err)
		}
	}
	alice := f.login(t, "alice")
	rootUser, _ := query.Objects[auth.User]().Get(f.ctx, func(u *auth.User) query.Cond { return query.Eq(&u.Username, "root") })
	if response, _ := alice.post("/admin/auth/user/"+strconv.FormatInt(rootUser.ID, 10)+"/", url.Values{"username": {"pwned"}}, nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("staff must not edit superusers: %d", response.StatusCode)
	}
	escalate := url.Values{"username": {"bob"}, "is_active": {"true"}, "is_staff": {"true"}, "is_superuser": {"true"}}
	if response, body := alice.post(userURL, escalate, nil); response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "only superusers can grant") {
		t.Fatalf("privilege escalation: %d", response.StatusCode)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	f := setup(t)
	bob := f.login(t, "bob")
	response, _ := bob.post(ticketURL(f.tickets["bob closed"])+"delete/", nil, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d", response.StatusCode)
	}
	if _, exists := f.reload(t, f.tickets["bob closed"]); !exists {
		t.Fatal("cross-site POST must not delete")
	}
}

func TestStaticAndRegistrationErrors(t *testing.T) {
	site := newSite()
	server := httptest.NewServer(site.Handler())
	defer server.Close()
	response, err := http.Get(server.URL + "/admin/static/admin.css")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("static: %v %v", err, response)
	}
	response.Body.Close()

	defer func() {
		recovered := recover()
		message, _ := recovered.(string)
		if !strings.Contains(message, "admin_test.go:") || !strings.Contains(message, "not a declared field") {
			t.Fatalf("expected a located configuration panic, got %v", recovered)
		}
	}()
	var stray string
	admin.Register(admin.NewSite("broken"), admin.ModelAdmin[Ticket]{
		Configure: func(_ *Ticket, config *admin.Config) { config.ListDisplay(admin.F(&stray)) },
	})
}
