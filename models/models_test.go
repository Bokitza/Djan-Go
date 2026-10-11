package models

import (
	"strings"
	"testing"
	"time"
)

type testAuthor struct {
	Model
	Name string
}

func (a *testAuthor) Schema(s *Schema) {
	s.App("library")
	s.Char(&a.Name, 100).Unique()
}

type testBook struct {
	Model
	Title     string
	Summary   *string
	Pages     int
	Published bool
	Rating    *float64
	ReleaseAt time.Time
	Author    FK[testAuthor]
	Editor    FK[testAuthor]
	Metadata  map[string]string
	Status    string
	cache     string
}

func (b *testBook) Schema(s *Schema) {
	s.App("library")
	s.Table("library_books")
	s.Ordering(Desc(&b.ReleaseAt), Asc(&b.Title))
	s.Char(&b.Title, 200).Index()
	s.NullableText(&b.Summary)
	s.Int(&b.Pages).Default(100)
	s.Bool(&b.Published).Default(false)
	s.NullableFloat(&b.Rating)
	s.DateTime(&b.ReleaseAt)
	ForeignKey(s, &b.Author).OnDelete(Protect)
	ForeignKey(s, &b.Editor).Nullable().OnDelete(SetNull)
	JSON(s, &b.Metadata)
	s.Char(&b.Status, 20).Choices("draft", "published").Default("draft")
	s.UniqueTogether(&b.Title, &b.Author)
}

func init() {
	Register[testAuthor]()
	Register[testBook]()
}

func TestSchemaResolution(t *testing.T) {
	MustValidate(t)
	info := MustInfo[testBook]()
	if info.Table != "library_books" || info.App != "library" {
		t.Fatalf("unexpected table/app %s %s", info.Table, info.App)
	}
	var columns []string
	for _, field := range info.Fields {
		columns = append(columns, field.Column)
	}
	want := "id title summary pages published rating release_at author_id editor_id metadata status created_at updated_at"
	if got := strings.Join(columns, " "); got != want {
		t.Fatalf("columns:\n got %s\nwant %s", got, want)
	}
	summary := info.FieldByName("summary")
	if !summary.Nullable || summary.Kind != KindText {
		t.Fatalf("summary should be nullable text: %+v", summary)
	}
	author := info.FieldByName("author")
	if author.Kind != KindForeignKey || author.Related() != MustInfo[testAuthor]() || author.OnDelete != Protect {
		t.Fatalf("bad foreign key %+v", author)
	}
	if info.Ordering[0].Field.Column != "release_at" || !info.Ordering[0].Descending {
		t.Fatalf("bad ordering %+v", info.Ordering)
	}
	if len(info.UniqueTogether) != 1 || info.UniqueTogether[0][1] != author {
		t.Fatalf("bad unique together %+v", info.UniqueTogether)
	}

	probe, _ := ProbeOf[testBook]()
	resolved, err := ResolvePointer(UnsafePointer(&probe.Author.ID), typeOf[int64]())
	if err != nil || resolved != author {
		t.Fatalf("Author.ID should resolve to the author field: %v %v", resolved, err)
	}
	if _, err := ResolvePointer(UnsafePointer(&probe.cache), typeOf[string]()); err == nil {
		t.Fatal("unexported field must not resolve")
	}
	var outside string
	if _, err := ResolvePointer(UnsafePointer(&outside), typeOf[string]()); err == nil || !strings.Contains(err.Error(), "closure argument") {
		t.Fatalf("pointer outside of a probe should fail clearly, got %v", err)
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	book := New[testBook]()
	if book.Pages != 100 || book.Status != "draft" {
		t.Fatalf("defaults not applied: %+v", book)
	}
}

func TestFieldValues(t *testing.T) {
	info := MustInfo[testBook]()
	book := &testBook{Author: Ref[testAuthor](7)}
	if got := info.FieldByName("author").Get(book); got != int64(7) {
		t.Fatalf("author id: %v", got)
	}
	if got := info.FieldByName("editor").Get(book); got != nil {
		t.Fatalf("zero nullable FK should be NULL, got %v", got)
	}
	if got := info.FieldByName("rating").Get(book); got != nil {
		t.Fatalf("nil pointer should be NULL, got %v", got)
	}
	if err := info.FieldByName("rating").Set(book, 4.5); err != nil || *book.Rating != 4.5 {
		t.Fatalf("set pointer field: %v", err)
	}
}

type missingDeclaration struct {
	Model
	Title string
	Body  string
}

func (m *missingDeclaration) Schema(s *Schema) { s.Char(&m.Title, 10) }

type declaredTwice struct {
	Model
	Title string
}

func (d *declaredTwice) Schema(s *Schema) {
	s.Char(&d.Title, 10)
	s.Text(&d.Title)
}

var foreignString string

type foreignPointer struct {
	Model
	Title string
}

func (f *foreignPointer) Schema(s *Schema) {
	s.Char(&foreignString, 10)
	s.Ignore(&f.Title)
}

type badDefault struct {
	Model
	Status string
}

func (b *badDefault) Schema(s *Schema) {
	s.Char(&b.Status, 10).Choices("a", "b").Default("c")
}

type noBase struct{ Title string }

func (n *noBase) Schema(s *Schema) {}

func TestStartupErrors(t *testing.T) {
	cases := []struct {
		name     string
		register func() (*ModelInfo, error)
		contains string
	}{
		{"undeclared", func() (*ModelInfo, error) { return register[missingDeclaration]("test") }, "Body is neither declared"},
		{"twice", func() (*ModelInfo, error) { return register[declaredTwice]("test") }, "declared twice"},
		{"foreign pointer", func() (*ModelInfo, error) { return register[foreignPointer]("test") }, "not a field of foreignPointer"},
		{"bad default", func() (*ModelInfo, error) { return register[badDefault]("test") }, "not one of its choices"},
		{"no base", func() (*ModelInfo, error) { return register[noBase]("test") }, "must embed models.Model"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := testCase.register()
			if err == nil || !strings.Contains(err.Error(), testCase.contains) {
				t.Fatalf("expected error containing %q, got %v", testCase.contains, err)
			}
			if !strings.Contains(err.Error(), "models_test.go:") && testCase.name != "undeclared" && testCase.name != "no base" {
				t.Fatalf("error should point at the declaring line: %v", err)
			}
		})
	}
}

func TestNaming(t *testing.T) {
	for input, want := range map[string]string{"CreatedAt": "created_at", "UserID": "user_id", "HTTPServer": "http_server", "Title": "title"} {
		if got := SnakeCase(input); got != want {
			t.Errorf("SnakeCase(%s) = %s, want %s", input, got, want)
		}
	}
	if Humanize("AuthorID") != "Author" || Pluralize("Category") != "Categories" {
		t.Error("humanize/pluralize")
	}
}
