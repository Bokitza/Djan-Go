package management_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yoav-rogat/djan-go/auth"
	"github.com/yoav-rogat/djan-go/testdb"
	"github.com/yoav-rogat/djan-go/management"
	"github.com/yoav-rogat/djan-go/models"
)

type Note struct {
	models.Model
	Text string
}

func (n *Note) Schema(s *models.Schema) {
	s.App("notes")
	s.Text(&n.Text)
}

func init() { models.Register[Note]() }

func TestCommands(t *testing.T) {
	ctx, pool := testdb.New(t)
	var output bytes.Buffer
	app := &management.App{
		DatabaseURL:   pool.Config().ConnString(),
		MigrationsDir: t.TempDir(),
		Stdout:        &output,
		Stderr:        &output,
	}
	run := func(arguments ...string) string {
		t.Helper()
		output.Reset()
		if err := app.Execute(ctx, arguments); err != nil {
			t.Fatalf("%v: %v\n%s", arguments, err, output.String())
		}
		return output.String()
	}

	if out := run("check"); !strings.Contains(out, "no issues") {
		t.Fatal(out)
	}
	if out := run("makemigrations", "-dry-run"); !strings.Contains(out, "Create table notes_note") || !strings.Contains(out, "CREATE TABLE") {
		t.Fatal(out)
	}
	if out := run("makemigrations"); !strings.Contains(out, "0001_initial.json") {
		t.Fatal(out)
	}
	if out := run("makemigrations"); !strings.Contains(out, "No changes detected") {
		t.Fatal(out)
	}
	if out := run("showmigrations"); !strings.Contains(out, "[ ] 0001_initial") {
		t.Fatal(out)
	}
	if out := run("migrate"); !strings.Contains(out, "Applying auth.0001_initial... OK") || !strings.Contains(out, "Applying project.0001_initial... OK") {
		t.Fatal(out)
	}
	if out := run("migrate"); !strings.Contains(out, "No migrations to apply") {
		t.Fatal(out)
	}

	app.Stdin = strings.NewReader("root\nroot@example.com\nlong-password\nlong-password\n")
	if out := run("createsuperuser"); !strings.Contains(out, `Superuser "root" created`) {
		t.Fatal(out)
	}
	if user, err := auth.Authenticate(ctx, "root", "long-password"); err != nil || !user.IsSuperuser || user.Email != "root@example.com" {
		t.Fatalf("superuser: %v %+v", err, user)
	}

	if out := run("migrate", "-rollback", "1"); !strings.Contains(out, "Unapplying project.0001_initial... OK") {
		t.Fatal(out)
	}
	if out := run("showmigrations"); !strings.Contains(out, "[X] 0001_initial") || !strings.Contains(out, "[ ] 0001_initial") {
		t.Fatal(out)
	}

	if err := app.Execute(ctx, []string{"bogus"}); err == nil {
		t.Fatal("unknown commands fail")
	}
}
