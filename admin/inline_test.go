package admin_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Bokitza/djan-go/admin"
	"github.com/Bokitza/djan-go/auth"
	"github.com/Bokitza/djan-go/models"
	"github.com/Bokitza/djan-go/query"
)

type Comment struct {
	models.Model
	Ticket   models.FK[Ticket]
	Body     string
	Internal bool
}

func (c *Comment) Schema(s *models.Schema) {
	s.App("helpdesk")
	s.Ordering(models.Asc(&c.Body))
	models.ForeignKey(s, &c.Ticket).OnDelete(models.Cascade)
	s.Char(&c.Body, 100)
	s.Bool(&c.Internal).Default(false)
}

func (c *Comment) String() string { return c.Body }

func init() { models.Register[Comment]() }

func commentSite() *admin.Site {
	site := newSite()
	admin.Unregister[Ticket](site)
	admin.Register(site, admin.ModelAdmin[Ticket]{
		Configure: func(t *Ticket, config *admin.Config) { config.ListDisplay(admin.F(&t.Title)) },
		ChangeQuerySet: func(r *admin.Request, tickets query.QuerySet[Ticket]) query.QuerySet[Ticket] {
			return tickets.Where(func(t *Ticket) query.Cond { return ownedBy(t, r.User) })
		},
		Inlines: []admin.Inline[Ticket]{
			admin.InlineAdmin[Ticket, Comment]{
				Label: "Comments",
				Extra: 1,
				Configure: func(c *Comment, config *admin.InlineConfig) {
					config.Fields(admin.F(&c.Body), admin.F(&c.Internal))
				},
				ShowChangeLink: true,
				Permissions: func(r *admin.Request, ticket *Ticket, granted admin.Permissions) admin.Permissions {
					granted.Add = granted.Add && ticket.Status == "open"
					return granted
				},
			},
		},
	})
	admin.Register(site, admin.ModelAdmin[Comment]{
		Configure: func(c *Comment, config *admin.Config) {
			admin.ReadonlyWhen(config, func(r *admin.Request, c *Comment) bool { return c.Body == "keep" }, admin.F(&c.Body))
		},
		BeforeSave: func(r *admin.Request, comment *Comment, isNew bool) error {
			if comment.Body == "forbidden" {
				return errors.New("comments cannot say forbidden")
			}
			return nil
		},
		ViewQuerySet: func(r *admin.Request, comments query.QuerySet[Comment]) query.QuerySet[Comment] {
			return comments.WhereNot(func(c *Comment) query.Cond { return query.Eq(&c.Internal, true) })
		},
		DeleteQuerySet: func(r *admin.Request, comments query.QuerySet[Comment]) query.QuerySet[Comment] {
			return comments.WhereNot(func(c *Comment) query.Cond { return query.Eq(&c.Body, "keep") })
		},
	})
	return site
}

func commentsOf(t *testing.T, f *fixture, ticket *Ticket) map[string]*Comment {
	t.Helper()
	comments, err := query.Objects[Comment]().Where(func(c *Comment) query.Cond { return query.Eq(&c.Ticket.ID, ticket.ID) }).All(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	byBody := map[string]*Comment{}
	for _, comment := range comments {
		byBody[comment.Body] = comment
	}
	return byBody
}

func TestInlineEditing(t *testing.T) {
	f := setup(t)
	for _, permission := range []string{"helpdesk.view_comment", "helpdesk.add_comment", "helpdesk.change_comment", "helpdesk.delete_comment", "auth.view_user"} {
		if err := auth.GrantPermission(f.ctx, f.alice, permission); err != nil {
			t.Fatal(err)
		}
	}
	ticket := f.tickets["alice open"]
	comments := map[string]*Comment{}
	for _, comment := range []*Comment{
		{Ticket: models.RefTo(ticket), Body: "first"},
		{Ticket: models.RefTo(ticket), Body: "keep"},
		{Ticket: models.RefTo(ticket), Body: "staff only", Internal: true},
		{Ticket: models.RefTo(f.tickets["bob open"]), Body: "elsewhere"},
	} {
		if err := query.Create(f.ctx, comment); err != nil {
			t.Fatal(err)
		}
		comments[comment.Body] = comment
	}
	server := httptest.NewServer(withPool(f.pool, commentSite().Handler()))
	t.Cleanup(server.Close)
	f.server = server
	alice := f.login(t, "alice")

	path := "/admin/helpdesk/ticket/" + strconv.FormatInt(ticket.ID, 10) + "/"
	_, body := alice.get(path, nil)
	first, keep := strconv.FormatInt(comments["first"].ID, 10), strconv.FormatInt(comments["keep"].ID, 10)
	for _, want := range []string{
		"Comments", `name="comment_ticket-TOTAL_FORMS" value="3"`,
		`name="comment_ticket-0-id" value="` + first + `"`, `name="comment_ticket-0-body" value="first"`,
		`name="comment_ticket-0-DELETE"`, `href="/admin/helpdesk/comment/` + first + `/"`,
		`name="comment_ticket-2-body" value=""`, `name="comment_ticket-__prefix__-body"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form is missing %q", want)
		}
	}
	for _, unwanted := range []string{"staff only", "elsewhere", `name="comment_ticket-1-DELETE"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("form must not contain %q", unwanted)
		}
	}

	ticketForm := func(extra url.Values) url.Values {
		form := url.Values{"title": {ticket.Title}, "owner": {strconv.FormatInt(f.alice.ID, 10)}, "status": {"open"}}
		for name, values := range extra {
			form[name] = values
		}
		return form
	}

	// A filled new row with a blank required field is an error and nothing is saved.
	response, body := alice.post(path, ticketForm(url.Values{
		"comment_ticket-TOTAL_FORMS": {"1"},
		"comment_ticket-0-id":        {""}, "comment_ticket-0-internal": {"true"},
	}), nil)
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Please correct the errors below.") || !strings.Contains(body, "This field is required.") {
		t.Fatalf("invalid row: status %d\n%s", response.StatusCode, body)
	}
	if got := len(commentsOf(t, f, ticket)); got != 3 {
		t.Fatalf("invalid row saved something: %d comments", got)
	}

	// The hooks of the comment admin run for inline rows and roll the whole save back.
	response, body = alice.post(path, ticketForm(url.Values{
		"title":                      {"renamed"},
		"comment_ticket-TOTAL_FORMS": {"1"},
		"comment_ticket-0-id":        {""}, "comment_ticket-0-body": {"forbidden"},
	}), nil)
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "comments cannot say forbidden") {
		t.Fatalf("BeforeSave of the child admin: status %d\n%s", response.StatusCode, body)
	}
	if stored, _ := query.Objects[Ticket]().GetByPK(f.ctx, ticket.ID); stored.Title != ticket.Title {
		t.Fatal("the ticket was saved although an inline row failed")
	}

	staffOnly := strconv.FormatInt(comments["staff only"].ID, 10)
	response, body = alice.post(path, ticketForm(url.Values{
		"comment_ticket-TOTAL_FORMS": {"6"},
		"comment_ticket-0-id":        {first}, "comment_ticket-0-body": {"first edited"},
		"comment_ticket-1-id": {keep}, "comment_ticket-1-body": {"keep changed"}, "comment_ticket-1-DELETE": {"on"},
		"comment_ticket-2-id": {staffOnly}, "comment_ticket-2-body": {"hacked"}, "comment_ticket-2-DELETE": {"on"},
		"comment_ticket-3-id": {""}, "comment_ticket-3-body": {"added"},
		"comment_ticket-4-id": {""}, "comment_ticket-4-body": {""},
		"comment_ticket-5-id": {strconv.FormatInt(comments["elsewhere"].ID, 10)}, "comment_ticket-5-body": {"moved"},
	}), nil)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: status %d\n%s", response.StatusCode, body)
	}
	saved := commentsOf(t, f, ticket)
	for _, want := range []string{"first edited", "keep", "staff only", "added"} {
		if saved[want] == nil {
			t.Errorf("comment %q is missing after save (have %v)", want, saved)
		}
	}
	if len(saved) != 4 {
		t.Errorf("after save: %d comments, want 4 (blank rows are skipped)", len(saved))
	}
	if elsewhere, err := query.Objects[Comment]().GetByPK(f.ctx, comments["elsewhere"].ID); err != nil || elsewhere.Body != "elsewhere" {
		t.Errorf("a comment of another ticket was changed: %v %v", elsewhere, err)
	}

	// Closed tickets offer no new rows.
	closed := f.tickets["alice secret"]
	_, body = alice.get("/admin/helpdesk/ticket/"+strconv.FormatInt(closed.ID, 10)+"/", nil)
	if !strings.Contains(body, "Comments") || strings.Contains(body, "__prefix__") {
		t.Error("closed tickets must show the inline without the add row")
	}
}

func TestInlineRegistrationErrors(t *testing.T) {
	expectPanic := func(name, want string, register func()) {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil || !strings.Contains(recovered.(string), want) {
					t.Fatalf("panic = %v, want it to mention %q", recovered, want)
				}
			}()
			register()
		})
	}
	expectPanic("no foreign key", "has no foreign key to Ticket", func() {
		admin.Register(admin.NewSite("x"), admin.ModelAdmin[Ticket]{Inlines: []admin.Inline[Ticket]{admin.InlineAdmin[Ticket, Label]{}}})
	})
	expectPanic("foreign key as a column", "set to the parent automatically", func() {
		admin.Register(admin.NewSite("x"), admin.ModelAdmin[Ticket]{Inlines: []admin.Inline[Ticket]{admin.InlineAdmin[Ticket, Comment]{
			Configure: func(c *Comment, config *admin.InlineConfig) { config.Fields(admin.F(&c.Ticket)) },
		}}})
	})
	expectPanic("listed twice", "listed twice", func() {
		inline := admin.InlineAdmin[Ticket, Comment]{}
		admin.Register(admin.NewSite("x"), admin.ModelAdmin[Ticket]{Inlines: []admin.Inline[Ticket]{inline, inline}})
	})
}
