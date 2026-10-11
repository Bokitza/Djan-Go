package admin_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yoav/djan-go/admin"
	"github.com/yoav/djan-go/auth"
	"github.com/yoav/djan-go/importexport"
	"github.com/yoav/djan-go/query"
)

var ticketResource = importexport.Resource[Ticket]{
	Name: "Tickets",
	Configure: func(t *Ticket, config *importexport.Config) {
		config.Fields(
			importexport.F(&t.ID),
			importexport.F(&t.Title),
			importexport.F(&t.Status),
			importexport.F(&t.Owner).Widget(importexport.ForeignKey(func(u *auth.User) *string { return &u.Username })),
			importexport.Through(&t.Owner, func(u *auth.User) importexport.Field { return importexport.F(&u.Email) }),
		)
	},
	BeforeImportRow: func(ctx context.Context, row importexport.Row) error {
		if request := admin.RequestFrom(ctx); request == nil || request.User == nil {
			return errors.New("no admin request in the import context")
		}
		return nil
	},
}

func (c *client) upload(path string, fields map[string]string, filename, content string) (*http.Response, string) {
	c.t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		_ = writer.WriteField(name, value)
	}
	if filename != "" {
		part, _ := writer.CreateFormFile("file", filename)
		_, _ = part.Write([]byte(content))
	}
	_ = writer.Close()
	request, _ := http.NewRequest(http.MethodPost, c.base+path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return c.do(request)
}

func TestListShowsImportAndExport(t *testing.T) {
	f := setup(t)
	_, body := f.login(t, "bob").get("/admin/helpdesk/ticket/", nil)
	for _, want := range []string{`href="/admin/helpdesk/ticket/import/"`, "Downloads every row matching", `name="format" value="csv"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("list page is missing %q", want)
		}
	}
}

func TestExportFollowsViewRules(t *testing.T) {
	f := setup(t)
	bob := f.login(t, "bob")
	response, body := bob.get("/admin/helpdesk/ticket/export/?format=csv&resource=0&f.status=closed", nil)
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Disposition"), `attachment; filename="ticket-`) {
		t.Fatalf("export: %d %v", response.StatusCode, response.Header)
	}
	want := fmt.Sprintf("id,title,status,owner,owner__email\n%d,bob closed,closed,bob,bob@example.com\n%d,bob closed too,closed,bob,bob@example.com\n",
		f.tickets["bob closed"].ID, f.tickets["bob closed too"].ID)
	if body != want {
		t.Fatalf("export:\n got %q\nwant %q", body, want)
	}

	form := url.Values{"format": {"json"}, "resource": {"0"}, "ids": {
		strconv.FormatInt(f.tickets["alice secret"].ID, 10), strconv.FormatInt(f.tickets["alice open"].ID, 10)}}
	response, body = bob.post("/admin/helpdesk/ticket/export/", form, nil)
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("export selected: %d %v", response.StatusCode, response.Header)
	}
	if !strings.Contains(body, `"alice open"`) || strings.Contains(body, "alice secret") {
		t.Fatalf("export selected must only contain visible rows: %s", body)
	}
}

var confirmData = regexp.MustCompile(`name="data" value="([^"]*)"`)

func TestImportPreviewAndConfirm(t *testing.T) {
	f := setup(t)
	if err := auth.GrantPermission(f.ctx, f.alice, "auth.view_user"); err != nil {
		t.Fatal(err)
	}
	alice := f.login(t, "alice")
	aliceOpen, bobOpen := f.tickets["alice open"], f.tickets["bob open"]

	response, body := alice.get("/admin/helpdesk/ticket/import/", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "owner__email") {
		t.Fatalf("import page: %d", response.StatusCode)
	}

	rejected := fmt.Sprintf("id,title,status,owner\n%d,alice renamed,open,alice\n%d,stolen,open,bob\n,bad status,pending,alice\n", aliceOpen.ID, bobOpen.ID)
	response, body = alice.upload("/admin/helpdesk/ticket/import/", map[string]string{"resource": "0"}, "tickets.csv", rejected)
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "permission to change this ticket") ||
		!strings.Contains(body, "Select a valid choice") || strings.Contains(body, "Confirm import") {
		t.Fatalf("preview with problems: %d\n%s", response.StatusCode, body)
	}
	if fresh, _ := f.reload(t, bobOpen); fresh.Title != "bob open" {
		t.Fatal("a rejected import changed data")
	}

	accepted := fmt.Sprintf("id,title,status,owner\n%d,alice renamed,open,alice\n,from import,closed,alice\n", aliceOpen.ID)
	response, body = alice.upload("/admin/helpdesk/ticket/import/", map[string]string{"resource": "0"}, "tickets.csv", accepted)
	match := confirmData.FindStringSubmatch(body)
	if response.StatusCode != http.StatusOK || match == nil {
		t.Fatalf("clean preview: %d\n%s", response.StatusCode, body)
	}
	if fresh, _ := f.reload(t, aliceOpen); fresh.Title != "alice open" {
		t.Fatal("the preview must not save")
	}

	response, _ = alice.upload("/admin/helpdesk/ticket/import/", map[string]string{
		"_confirm": "1", "resource": "0", "filename": "tickets.csv", "data": html.UnescapeString(match[1])}, "", "")
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/admin/helpdesk/ticket/" {
		t.Fatalf("confirm: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	if fresh, _ := f.reload(t, aliceOpen); fresh.Title != "alice renamed" {
		t.Fatalf("confirmed import did not update: %q", fresh.Title)
	}
	created, err := query.Objects[Ticket]().Get(f.ctx, func(ticket *Ticket) query.Cond { return query.Eq(&ticket.Title, "from import") })
	if err != nil || created.Owner.ID != f.alice.ID || created.Status != "closed" {
		t.Fatalf("created ticket: %+v %v", created, err)
	}
}

func TestImportNeedsPermissionsAndKnownFormats(t *testing.T) {
	f := setup(t)
	carol := createStaff(t, f.ctx, "carol")
	if err := auth.GrantPermission(f.ctx, carol, "helpdesk.view_ticket"); err != nil {
		t.Fatal(err)
	}
	if response, _ := f.login(t, "carol").get("/admin/helpdesk/ticket/import/", nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("import without add or change permission: %d", response.StatusCode)
	}
	response, body := f.login(t, "alice").upload("/admin/helpdesk/ticket/import/", map[string]string{"resource": "0"}, "tickets.xlsx", "x")
	if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Unsupported file type") {
		t.Fatalf("unsupported format: %d", response.StatusCode)
	}
}
