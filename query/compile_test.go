package query

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Bokitza/djan-go/models"
)

type writer struct {
	models.Model
	Name  string
	Email string
}

func (w *writer) Schema(s *models.Schema) {
	s.App("press")
	s.Char(&w.Name, 100)
	s.Char(&w.Email, 200).Unique()
}

type article struct {
	models.Model
	Title     string
	Published bool
	Score     *int
	Author    models.FK[writer]
	Editor    models.FK[writer]
}

func (a *article) Schema(s *models.Schema) {
	s.App("press")
	s.Ordering(models.Desc(&a.CreatedAt))
	s.Char(&a.Title, 200)
	s.Bool(&a.Published)
	s.NullableInt(&a.Score)
	models.ForeignKey(s, &a.Author)
	models.ForeignKey(s, &a.Editor).Nullable().OnDelete(models.SetNull)
}

func init() {
	models.Register[writer]()
	models.Register[article]()
}

func normalize(sql string) string { return strings.Join(strings.Fields(sql), " ") }

const articleColumns = `"t0"."id", "t0"."title", "t0"."published", "t0"."score", "t0"."author_id", "t0"."editor_id", "t0"."created_at", "t0"."updated_at"`

func TestGoldenSQL(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		queryset  QuerySet[article]
		sql       string
		arguments []any
	}{
		{
			name:     "default ordering",
			queryset: Objects[article](),
			sql:      `SELECT ` + articleColumns + ` FROM "press_article" AS "t0" WHERE TRUE ORDER BY "t0"."created_at" DESC, "t0"."id" ASC`,
		},
		{
			name: "typed lookups",
			queryset: Objects[article]().Where(func(a *article) Cond {
				return And(
					Eq(&a.Published, true),
					Gte(Nullable(&a.Score), 5),
					Not(IContains(&a.Title, "50%_off")),
					Lt(&a.CreatedAt, cutoff),
				)
			}).OrderBy(func(a *article) []Order { return By(Asc(&a.Title)) }).Limit(10).Offset(20),
			sql:       `SELECT ` + articleColumns + ` FROM "press_article" AS "t0" WHERE ("t0"."published" = $1 AND "t0"."score" >= $2 AND NOT ("t0"."title" ILIKE $3) AND "t0"."created_at" < $4) ORDER BY "t0"."title" ASC, "t0"."id" ASC LIMIT 10 OFFSET 20`,
			arguments: []any{true, int64(5), `%50\%\_off%`, cutoff},
		},
		{
			name: "relation and or",
			queryset: Objects[article]().Where(func(a *article) Cond {
				return Or(
					Rel(&a.Author, func(w *writer) Cond { return IExact(&w.Email, "A@B.C") }),
					IsNull(&a.Score, true),
					IsNullFK(&a.Editor, false),
					Eq(&a.Author.ID, 3),
					In(&a.ID, 1, 2),
				)
			}),
			sql:       `SELECT ` + articleColumns + ` FROM "press_article" AS "t0" WHERE ("t0"."author_id" IN (SELECT "t1"."id" FROM "press_writer" AS "t1" WHERE lower("t1"."email") = lower($1)) OR "t0"."score" IS NULL OR "t0"."editor_id" IS NOT NULL OR "t0"."author_id" = $2 OR "t0"."id" IN ($3, $4)) ORDER BY "t0"."created_at" DESC, "t0"."id" ASC`,
			arguments: []any{"A@B.C", int64(3), int64(1), int64(2)},
		},
		{
			name:      "string fallback",
			queryset:  Objects[article]().Filter(Q{"author__name__startswith": "Yo", "published": false}).OrderByNames("-title"),
			sql:       `SELECT ` + articleColumns + ` FROM "press_article" AS "t0" WHERE ("t0"."author_id" IN (SELECT "t1"."id" FROM "press_writer" AS "t1" WHERE "t1"."name" LIKE $1) AND "t0"."published" = $2) ORDER BY "t0"."title" DESC, "t0"."id" ASC`,
			arguments: []any{"Yo%", false},
		},
		{
			name:     "empty in",
			queryset: Objects[article]().Where(func(a *article) Cond { return In(&a.ID) }).OrderByNames(),
			sql:      `SELECT ` + articleColumns + ` FROM "press_article" AS "t0" WHERE FALSE`,
		},
		{
			name: "raw",
			queryset: Objects[article]().Filter(Raw(`"t0"."score" % $1 = 0`, 2)).Where(func(a *article) Cond {
				return Eq(&a.Title, "x")
			}).OrderByNames(),
			sql:       `SELECT ` + articleColumns + ` FROM "press_article" AS "t0" WHERE (("t0"."score" % $1 = 0) AND "t0"."title" = $2)`,
			arguments: []any{2, "x"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sql, arguments, err := testCase.queryset.ToSQL()
			if err != nil {
				t.Fatal(err)
			}
			if normalize(sql) != normalize(testCase.sql) {
				t.Fatalf("sql:\n got %s\nwant %s", sql, testCase.sql)
			}
			if len(arguments) != len(testCase.arguments) || (len(arguments) > 0 && !reflect.DeepEqual(arguments, testCase.arguments)) {
				t.Fatalf("arguments: got %#v want %#v", arguments, testCase.arguments)
			}
		})
	}
}

func TestQueryErrors(t *testing.T) {
	var outside string
	cases := map[string]QuerySet[article]{
		"unknown field":   Objects[article]().Filter(Q{"nope": 1}),
		"unknown lookup":  Objects[article]().Filter(Q{"title__nope": 1}),
		"text lookup":     Objects[article]().Filter(Q{"published__contains": "x"}),
		"wrong model":     Objects[article]().Where(func(a *article) Cond { return Eq(&MustProbe[writer]().Name, "x") }),
		"outside pointer": Objects[article]().Where(func(a *article) Cond { return Eq(&outside, "x") }),
		"bad order":       Objects[article]().OrderByNames("nope"),
	}
	for name, queryset := range cases {
		if _, _, err := queryset.ToSQL(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func MustProbe[T any]() *T {
	probe, err := models.ProbeOf[T]()
	if err != nil {
		panic(err)
	}
	return probe
}
