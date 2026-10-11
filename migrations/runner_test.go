package migrations_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoav/djan-go/auth"
	"github.com/yoav/djan-go/internal/testdb"
	"github.com/yoav/djan-go/migrations"
	"github.com/yoav/djan-go/models"
	"github.com/yoav/djan-go/query"
)

type note struct {
	models.Model
	Body   string
	Pinned bool
	Owner  models.FK[auth.User]
}

func (n *note) Schema(s *models.Schema) {
	s.App("notes")
	s.Text(&n.Body)
	s.Bool(&n.Pinned).Default(false)
	models.ForeignKey(s, &n.Owner)
}

func init() { models.Register[note]() }

func TestMakeAndApplyMigrations(t *testing.T) {
	ctx, pool := testdb.New(t)
	directory := t.TempDir()
	sources := migrations.ProjectSources(directory)
	project := sources[len(sources)-1]

	migration, path, err := migrations.MakeMigration(project, "", true)
	if err != nil || migration == nil {
		t.Fatalf("makemigrations: %v", err)
	}
	if filepath.Base(path) != "0001_initial.json" {
		t.Fatalf("unexpected file %s", path)
	}
	for _, operation := range migration.Operations {
		if strings.HasPrefix(operation.Table, "auth_") || (operation.TableState != nil && strings.HasPrefix(operation.TableState.Name, "auth_")) {
			t.Fatalf("project migration must not own auth tables: %s", operation.Describe())
		}
	}
	if again, _, err := migrations.MakeMigration(project, "", false); err != nil || again != nil {
		t.Fatalf("second makemigrations should detect nothing: %v %v", again, err)
	}

	applied, err := migrations.Migrate(ctx, pool, sources, nil)
	if err != nil || applied != 2 {
		t.Fatalf("migrate applied %d: %v", applied, err)
	}
	if applied, err := migrations.Migrate(ctx, pool, sources, nil); err != nil || applied != 0 {
		t.Fatalf("migrate should be idempotent: %d %v", applied, err)
	}

	owner, err := auth.CreateUser(ctx, "owner", "", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := query.Create(ctx, &note{Body: "hi", Owner: models.RefTo(owner)}); err != nil {
		t.Fatal(err)
	}
	permissions, err := query.Objects[auth.Permission]().Where(func(p *auth.Permission) query.Cond {
		return query.Eq(&p.App, "notes")
	}).Count(ctx)
	if err != nil || permissions != 4 {
		t.Fatalf("post-migrate hook should create 4 permissions for notes, got %d (%v)", permissions, err)
	}

	pending, err := migrations.Pending(ctx, pool, sources)
	if err != nil || pending != 0 {
		t.Fatalf("pending %d %v", pending, err)
	}

	edited := filepath.Join(directory, "0002_custom.json")
	custom := migrations.NewMigration("0002_custom", []migrations.Operation{{Type: migrations.RunSQL,
		SQL: "CREATE INDEX notes_note_body_idx ON notes_note (body)", ReverseSQL: "DROP INDEX notes_note_body_idx"}})
	writeJSON(t, edited, custom)
	if applied, err := migrations.Migrate(ctx, pool, sources, nil); err != nil || applied != 1 {
		t.Fatalf("run_sql migration: %d %v", applied, err)
	}
	if rolled, err := migrations.Rollback(ctx, pool, project, 2, nil); err != nil || rolled != 2 {
		t.Fatalf("rollback: %d %v", rolled, err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('notes_note') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("rollback should drop the table: %v %v", exists, err)
	}
}

func writeJSON(t *testing.T, path string, migration *migrations.Migration) {
	t.Helper()
	content, err := json.Marshal(migration)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}
