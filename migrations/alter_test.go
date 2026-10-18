package migrations_test

import (
	"path/filepath"
	"testing"

	"github.com/Bokitza/djan-go/migrations"
	"github.com/Bokitza/djan-go/testdb"
)

// TestAlterationsKeepData applies and rolls back the alterations that SQLite implements by
// rebuilding the table, on whichever database testdb provides.
func TestAlterationsKeepData(t *testing.T) {
	ctx, database := testdb.New(t)
	directory := t.TempDir()
	sources := migrations.ProjectSources(directory)
	project := sources[len(sources)-1]
	execute := func(statement string) error {
		_, err := database.Database().ExecContext(ctx, statement)
		return err
	}
	mustExecute := func(statement string) {
		t.Helper()
		if err := execute(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	count := func(statement string) int {
		t.Helper()
		var result int
		if err := database.Database().QueryRowContext(ctx, statement).Scan(&result); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
		return result
	}
	migrate := func(want int) {
		t.Helper()
		if applied, err := migrations.Migrate(ctx, database, sources, nil); err != nil || applied != want {
			t.Fatalf("migrate applied %d (want %d): %v", applied, want, err)
		}
	}

	author := &migrations.Table{Name: "shop_author", Columns: []*migrations.Column{
		{Name: "id", Type: "bigint", PrimaryKey: true},
		{Name: "name", Type: "text"},
	}}
	post := &migrations.Table{Name: "shop_post", Columns: []*migrations.Column{
		{Name: "id", Type: "bigint", PrimaryKey: true},
		{Name: "title", Type: "varchar(200)"},
		{Name: "author_id", Type: "bigint"},
	}}
	authorKey := &migrations.Column{Name: "author_id", Type: "bigint",
		ForeignKey: &migrations.ForeignKey{Table: "shop_author", Column: "id", OnDelete: "CASCADE"}}
	writeJSON(t, filepath.Join(directory, "0001_initial.json"), migrations.NewMigration("0001_initial", []migrations.Operation{
		{Type: migrations.CreateTable, TableState: author},
		{Type: migrations.CreateTable, TableState: post},
		{Type: migrations.AddIndex, Table: "shop_post", Index: &migrations.Index{Name: "shop_post_author_id_idx", Columns: []string{"author_id"}}},
		{Type: migrations.AddForeignKey, Table: "shop_post", Column: authorKey},
	}))
	migrate(2)
	mustExecute("INSERT INTO shop_author (id, name) VALUES (1, 'Ada')")
	mustExecute("INSERT INTO shop_post (id, title, author_id) VALUES (1, 'Engines', 1)")
	if execute("INSERT INTO shop_post (id, title, author_id) VALUES (2, 'Orphan', 999)") == nil {
		t.Fatal("the foreign key should reject a missing author")
	}

	writeJSON(t, filepath.Join(directory, "0002_alter.json"), migrations.NewMigration("0002_alter", []migrations.Operation{
		{Type: migrations.DropForeignKey, Table: "shop_post", Column: authorKey},
		{Type: migrations.AddColumn, Table: "shop_post", Column: &migrations.Column{Name: "views", Type: "bigint", Default: "0"}},
		{Type: migrations.AddColumn, Table: "shop_post", Column: &migrations.Column{Name: "slug", Type: "varchar(50)"}},
		{Type: migrations.AddColumn, Table: "shop_post", Column: &migrations.Column{Name: "note", Type: "text", Nullable: true}},
		{Type: migrations.AlterColumn, Table: "shop_post",
			Previous: &migrations.Column{Name: "title", Type: "varchar(200)"},
			Column:   &migrations.Column{Name: "title", Type: "text", Nullable: true}},
		{Type: migrations.AddIndex, Table: "shop_post", Index: &migrations.Index{Name: "shop_post_slug_key", Columns: []string{"slug"}, Unique: true}},
	}))
	migrate(1)
	if got := count("SELECT count(*) FROM shop_post WHERE id = 1 AND title = 'Engines' AND views = 0 AND slug = '' AND note IS NULL"); got != 1 {
		t.Fatal("the existing row should survive with zero values in the new columns")
	}
	mustExecute("INSERT INTO shop_post (id, title, author_id, slug) VALUES (2, NULL, 999, 'orphan')")
	if execute("INSERT INTO shop_post (id, author_id, slug) VALUES (3, 1, 'orphan')") == nil {
		t.Fatal("the unique index should be recreated")
	}
	mustExecute("DELETE FROM shop_post WHERE id = 2")

	writeJSON(t, filepath.Join(directory, "0003_drop.json"), migrations.NewMigration("0003_drop", []migrations.Operation{
		{Type: migrations.DropColumn, Table: "shop_post", Previous: &migrations.Column{Name: "views", Type: "bigint", Default: "0"}},
	}))
	migrate(1)
	if execute("SELECT views FROM shop_post") == nil {
		t.Fatal("the column should be dropped")
	}

	if rolled, err := migrations.Rollback(ctx, database, project, 2, nil); err != nil || rolled != 2 {
		t.Fatalf("rollback: %d %v", rolled, err)
	}
	if got := count("SELECT count(*) FROM shop_post WHERE id = 1 AND title = 'Engines' AND author_id = 1"); got != 1 {
		t.Fatal("rolling back should keep the row")
	}
	if execute("SELECT slug FROM shop_post") == nil {
		t.Fatal("rolling back should drop the added column")
	}
	if execute("INSERT INTO shop_post (id, title, author_id) VALUES (4, 'Orphan', 999)") == nil {
		t.Fatal("rolling back should restore the foreign key")
	}
	if execute("INSERT INTO shop_post (id, author_id) VALUES (5, 1)") == nil {
		t.Fatal("rolling back should restore NOT NULL")
	}
	mustExecute("DELETE FROM shop_author WHERE id = 1")
	if got := count("SELECT count(*) FROM shop_post"); got != 0 {
		t.Fatal("the restored foreign key should cascade")
	}
}
