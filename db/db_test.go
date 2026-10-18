package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Bokitza/djan-go/db"
	"github.com/Bokitza/djan-go/testdb"
)

func TestNestedAtomicUsesSavepoints(t *testing.T) {
	ctx, database := testdb.New(t)
	execute := func(ctx context.Context, statement string) error {
		executor, err := db.FromContext(ctx)
		if err != nil {
			return err
		}
		_, err = executor.ExecContext(ctx, statement)
		return err
	}
	if err := execute(ctx, "CREATE TABLE items (name text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("inner failure")
	err := db.Atomic(ctx, func(ctx context.Context) error {
		if err := execute(ctx, "INSERT INTO items (name) VALUES ('outer')"); err != nil {
			return err
		}
		err := db.Atomic(ctx, func(ctx context.Context) error {
			if err := execute(ctx, "INSERT INTO items (name) VALUES ('inner')"); err != nil {
				return err
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("inner error: %v", err)
		}
		return db.Atomic(ctx, func(ctx context.Context) error {
			return execute(ctx, "INSERT INTO items (name) VALUES ('second')")
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := database.Database().QueryContext(ctx, "SELECT name FROM items ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if len(names) != 2 || names[0] != "outer" || names[1] != "second" {
		t.Fatalf("the failed savepoint should roll back only its own insert, got %v", names)
	}
}
