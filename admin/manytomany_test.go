package admin_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Bokitza/djan-go/admin"
	"github.com/Bokitza/djan-go/auth"
	"github.com/Bokitza/djan-go/models"
	"github.com/Bokitza/djan-go/query"
)

type Label struct {
	models.Model
	Name string
}

func (l *Label) Schema(s *models.Schema) {
	s.App("helpdesk")
	s.Ordering(models.Asc(&l.Name))
	s.Char(&l.Name, 50)
}

func (l *Label) String() string { return l.Name }

type TicketLabel struct {
	models.Model
	Ticket models.FK[Ticket]
	Label  models.FK[Label]
}

func (t *TicketLabel) Schema(s *models.Schema) {
	s.App("helpdesk")
	models.ForeignKey(s, &t.Ticket)
	models.ForeignKey(s, &t.Label)
	s.UniqueTogether(&t.Ticket, &t.Label)
}

func init() {
	models.Register[Label]()
	models.Register[TicketLabel]()
}

func labelSite(language string) *admin.Site {
	site := admin.NewSite("Helpdesk")
	site.Language = language
	admin.Register(site, admin.ModelAdmin[Ticket]{
		Configure: func(t *Ticket, config *admin.Config) { config.ListDisplay(admin.F(&t.Title)) },
		ManyToMany: []admin.ManyToManyField[Ticket]{
			admin.ManyToMany("labels", "Labels",
				func(link *TicketLabel) *models.FK[Ticket] { return &link.Ticket },
				func(link *TicketLabel) *models.FK[Label] { return &link.Label }).Help("Pick any number of labels."),
		},
	})
	admin.Register(site, admin.ModelAdmin[Label]{
		ViewQuerySet: func(r *admin.Request, labels query.QuerySet[Label]) query.QuerySet[Label] {
			if r.User.IsSuperuser {
				return labels
			}
			return labels.WhereNot(func(l *Label) query.Cond { return query.Eq(&l.Name, "hidden") })
		},
	})
	return site
}

func TestManyToManyEditing(t *testing.T) {
	f := setup(t)
	for _, permission := range []string{"helpdesk.view_label", "auth.view_user"} {
		if err := auth.GrantPermission(f.ctx, f.alice, permission); err != nil {
			t.Fatal(err)
		}
	}
	labels := map[string]*Label{}
	for _, name := range []string{"bug", "feature", "hidden"} {
		label := &Label{Name: name}
		if err := query.Create(f.ctx, label); err != nil {
			t.Fatal(err)
		}
		labels[name] = label
	}
	ticket := f.tickets["alice open"]
	for _, name := range []string{"bug", "hidden"} {
		if err := query.Create(f.ctx, &TicketLabel{Ticket: models.RefTo(ticket), Label: models.RefTo(labels[name])}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(withPool(f.pool, labelSite("").Handler()))
	t.Cleanup(server.Close)
	f.server = server
	alice := f.login(t, "alice")

	path := "/admin/helpdesk/ticket/" + strconv.FormatInt(ticket.ID, 10) + "/"
	_, body := alice.get(path, nil)
	for _, want := range []string{"Labels", "Pick any number of labels.", `value="` + strconv.FormatInt(labels["bug"].ID, 10) + `" checked`, ">feature<"} {
		if !strings.Contains(body, want) {
			t.Errorf("form is missing %q", want)
		}
	}
	if strings.Contains(body, ">hidden<") {
		t.Error("labels the user cannot view must not be offered")
	}

	form := url.Values{
		"title": {ticket.Title}, "owner": {strconv.FormatInt(f.alice.ID, 10)}, "status": {"open"},
		"labels": {strconv.FormatInt(labels["feature"].ID, 10), strconv.FormatInt(labels["hidden"].ID, 10)},
	}
	response, body := alice.post(path, form, nil)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: status %d\n%s", response.StatusCode, body)
	}
	links, err := query.Objects[TicketLabel]().Where(func(link *TicketLabel) query.Cond { return query.Eq(&link.Ticket.ID, ticket.ID) }).All(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var linked []int64
	for _, link := range links {
		linked = append(linked, link.Label.ID)
	}
	slices.Sort(linked)
	want := []int64{labels["feature"].ID, labels["hidden"].ID}
	slices.Sort(want)
	if !slices.Equal(linked, want) {
		t.Fatalf("links after save: %v, want feature and the untouched hidden link %v", linked, want)
	}
}

func TestHebrewAdminIsRightToLeft(t *testing.T) {
	f := setup(t)
	server := httptest.NewServer(withPool(f.pool, labelSite("he").Handler()))
	t.Cleanup(server.Close)
	f.server = server
	_, body := f.anonymous(t).get("/admin/login/", nil)
	for _, want := range []string{`lang="he" dir="rtl"`, "כניסה ל", "שם משתמש"} {
		if !strings.Contains(body, want) {
			t.Errorf("login page is missing %q", want)
		}
	}
}
