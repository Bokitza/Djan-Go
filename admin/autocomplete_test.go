package admin_test

import (
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

func withoutUser(username string) admin.RuleFunc[auth.User] {
	return func(r *admin.Request, users query.QuerySet[auth.User]) query.QuerySet[auth.User] {
		return users.WhereNot(func(u *auth.User) query.Cond { return query.Eq(&u.Username, username) })
	}
}

func autocompleteSite() *admin.Site {
	site := newSite()
	admin.Unregister[Ticket](site)
	admin.Register(site, admin.ModelAdmin[Ticket]{
		Configure: func(t *Ticket, config *admin.Config) {
			config.ListDisplay(admin.F(&t.Title), admin.F(&t.Owner))
			config.ListFilter(admin.F(&t.Status))
			admin.AutocompleteFilter(config, &t.Owner, withoutUser("carol"))
			admin.RelatedQuerySet(config, &t.Owner, withoutUser("alice"))
		},
	})
	return site
}

func TestAutocompleteFilter(t *testing.T) {
	f := setup(t)
	createStaff(t, f.ctx, "carol")
	if err := auth.GrantPermission(f.ctx, f.alice, "auth.view_user"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(withDatabase(f.database, autocompleteSite().Handler()))
	t.Cleanup(server.Close)
	f.server = server
	alice := f.login(t, "alice")

	_, body := alice.get("/admin/helpdesk/ticket/", nil)
	searchURL := `/admin/auth/user/autocomplete/?lookup=helpdesk%2Fticket%2Ffilter%2Fowner`
	if !strings.Contains(body, `hx-get="`+searchURL+`"`) || !strings.Contains(body, "autocompleteFilter(") {
		t.Fatal("list must offer an autocomplete filter for the owner")
	}
	if strings.Contains(body, `f.owner=`+strconv.FormatInt(f.bob.ID, 10)) {
		t.Fatal("autocomplete filters must not list every related row")
	}

	_, body = alice.get("/admin/auth/user/autocomplete/?lookup=helpdesk/ticket/filter/owner", nil)
	if !strings.Contains(body, "alice") || !strings.Contains(body, "bob") || strings.Contains(body, "carol") {
		t.Fatalf("filter search must apply its queryset: %s", body)
	}
	_, body = alice.get("/admin/auth/user/autocomplete/?lookup=helpdesk/ticket/filter/owner&q=bo", nil)
	if !strings.Contains(body, "bob") || strings.Contains(body, ">alice<") {
		t.Fatalf("filter search must search: %s", body)
	}
	if response, _ := alice.get("/admin/auth/user/autocomplete/?lookup=nope", nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown lookup: %d", response.StatusCode)
	}
	if response, _ := alice.get("/admin/auth/group/autocomplete/?lookup=helpdesk/ticket/filter/owner", nil); response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusForbidden {
		t.Fatalf("lookup of another model: %d", response.StatusCode)
	}

	_, body = alice.get("/admin/helpdesk/ticket/?f.owner="+strconv.FormatInt(f.bob.ID, 10), nil)
	if !strings.Contains(body, "bob open") || strings.Contains(body, "alice open") {
		t.Fatal("filtering by owner")
	}
	if !strings.Contains(body, "Owner: bob") {
		t.Fatal("the active filter shows the label of the related row")
	}
}

func TestRelatedQuerySet(t *testing.T) {
	f := setup(t)
	if err := auth.GrantPermission(f.ctx, f.alice, "auth.view_user"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(withDatabase(f.database, autocompleteSite().Handler()))
	t.Cleanup(server.Close)
	f.server = server
	alice := f.login(t, "alice")

	ticket := f.tickets["alice open"]
	_, body := alice.get(ticketURL(ticket), nil)
	if !strings.Contains(body, `hx-get="/admin/auth/user/autocomplete/?lookup=helpdesk%2Fticket%2Fform%2Fowner"`) {
		t.Fatal("the owner field must search with its queryset")
	}
	_, body = alice.get("/admin/auth/user/autocomplete/?lookup=helpdesk/ticket/form/owner", nil)
	if !strings.Contains(body, "bob") || strings.Contains(body, ">alice<") {
		t.Fatalf("form search must apply its queryset: %s", body)
	}

	form := url.Values{"title": {"renamed"}, "owner": {strconv.FormatInt(f.alice.ID, 10)}, "status": {"open"}}
	response, _ := alice.post(ticketURL(ticket), form, nil)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("keeping a stored owner outside the queryset is allowed: %d", response.StatusCode)
	}

	other := &Ticket{Title: "new", Owner: models.RefTo(f.bob), Status: "open"}
	if err := query.Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	form = url.Values{"title": {"new"}, "owner": {strconv.FormatInt(f.alice.ID, 10)}, "status": {"open"}}
	response, body = alice.post(ticketURL(other), form, nil)
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "not one of the available choices") {
		t.Fatalf("picking an owner outside the queryset: %d", response.StatusCode)
	}
	if fresh, _ := f.reload(t, other); fresh.Owner.ID != f.bob.ID {
		t.Fatal("the owner must not change")
	}
}

func TestRelatedQuerySetRegistrationErrors(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil || !strings.Contains(recovered.(string), "is filtered twice") {
			t.Fatalf("expected a registration error, got %v", recovered)
		}
	}()
	site := admin.NewSite("Broken")
	admin.Register(site, admin.ModelAdmin[Ticket]{
		Configure: func(t *Ticket, config *admin.Config) {
			config.ListFilter(admin.F(&t.Owner))
			admin.AutocompleteFilter(config, &t.Owner, nil)
		},
	})
}
