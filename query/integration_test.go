package query

import (
	"context"
	"errors"
	"testing"

	"github.com/yoav/djan-go/db"
	"github.com/yoav/djan-go/internal/testdb"
	"github.com/yoav/djan-go/models"
)

func TestQuerySetsAgainstPostgres(t *testing.T) {
	ctx, _ := testdb.New(t)
	testdb.CreateTables(t, ctx)

	ada := &writer{Name: "Ada", Email: "ada@example.com"}
	linus := &writer{Name: "Linus", Email: "linus@example.com"}
	for _, author := range []*writer{ada, linus} {
		if err := Create(ctx, author); err != nil {
			t.Fatal(err)
		}
	}
	if ada.ID == 0 || ada.CreatedAt.IsZero() {
		t.Fatalf("insert should fill id and timestamps: %+v", ada)
	}
	five := 5
	articles := []*article{
		{Title: "Engines", Published: true, Score: &five, Author: models.RefTo(ada)},
		{Title: "Kernels", Published: true, Author: models.RefTo(linus), Editor: models.RefTo(ada)},
		{Title: "Drafts", Published: false, Author: models.RefTo(ada)},
	}
	for _, item := range articles {
		if err := Save(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	byAda, err := Objects[article]().Where(func(a *article) Cond {
		return Rel(&a.Author, func(w *writer) Cond { return IStartsWith(&w.Name, "ad") })
	}).OrderBy(func(a *article) []Order { return By(Asc(&a.Title)) }).All(ctx)
	if err != nil || len(byAda) != 2 || byAda[0].Title != "Drafts" {
		t.Fatalf("relation filter: %v %v", byAda, err)
	}

	scored, err := Objects[article]().Where(func(a *article) Cond { return Gte(Nullable(&a.Score), 3) }).Get(ctx, nil)
	if err != nil || scored.Title != "Engines" || *scored.Score != 5 {
		t.Fatalf("nullable lookup: %+v %v", scored, err)
	}
	noEditor, err := Objects[article]().Where(func(a *article) Cond { return IsNullFK(&a.Editor, true) }).Count(ctx)
	if err != nil || noEditor != 2 {
		t.Fatalf("null FK count: %d %v", noEditor, err)
	}

	editor, err := Objects[article]().Where(func(a *article) Cond { return Eq(&a.Title, "Kernels") }).Get(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := editor.Editor.Get(ctx)
	if err != nil || loaded.Name != "Ada" {
		t.Fatalf("FK.Get: %v %v", loaded, err)
	}

	preloaded, err := Objects[article]().Preload(ctx, func(a *article) []Relation { return []Relation{Related(&a.Author)} })
	if err != nil || preloaded[0].Author.Cached() == nil {
		t.Fatalf("preload: %v", err)
	}

	updated, err := Objects[article]().Where(func(a *article) Cond { return Eq(&a.Published, false) }).
		Update(ctx, func(a *article) []Assign { return []Assign{Set(&a.Published, true), Set(&a.Score, &five)} })
	if err != nil || updated != 1 {
		t.Fatalf("update: %d %v", updated, err)
	}

	values, err := Objects[article]().Filter(Q{"author__email__iexact": "LINUS@example.com"}).Values(ctx, "title", "author")
	if err != nil || len(values) != 1 || values[0]["title"] != "Kernels" || values[0]["author"] != linus.ID {
		t.Fatalf("values with string fallback: %v %v", values, err)
	}

	count := 0
	for item, err := range Objects[article]().Iter(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if item.Title != "" {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("iterated %d", count)
	}

	rollback := errors.New("rollback")
	err = db.Atomic(ctx, func(ctx2 context.Context) error {
		if _, err := Objects[article]().Delete(ctx2); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if total, _ := Objects[article]().Count(ctx); total != 3 {
		t.Fatalf("atomic rollback should keep rows, got %d", total)
	}

	if err := DeleteObject(ctx, articles[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := Objects[article]().GetByPK(ctx, articles[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := Objects[article]().Get(ctx, nil); !errors.Is(err, ErrMultipleObjects) {
		t.Fatalf("expected ErrMultipleObjects, got %v", err)
	}
}
