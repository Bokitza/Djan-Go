package importexport_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Bokitza/djan-go/importexport"
	"github.com/Bokitza/djan-go/models"
	"github.com/Bokitza/djan-go/query"
	"github.com/Bokitza/djan-go/testdb"
)

type Publisher struct {
	models.Model
	Name string
}

func (p *Publisher) Schema(s *models.Schema) { s.Char(&p.Name, 100).Unique() }

type Book struct {
	models.Model
	Title     string
	ISBN      string
	Pages     int
	Price     *float64
	Genre     string
	Publisher models.FK[Publisher]
}

func (b *Book) Schema(s *models.Schema) {
	s.Ordering(models.Asc(&b.Title))
	s.Char(&b.Title, 200)
	s.Char(&b.ISBN, 13).Unique()
	s.Int(&b.Pages).Default(0)
	s.NullableFloat(&b.Price)
	s.Char(&b.Genre, 20).Choices("fiction", "science").Default("fiction")
	models.ForeignKey(s, &b.Publisher).Nullable()
}

func (b *Book) String() string { return b.Title }

func (b *Book) Validate() error {
	if b.Pages < 0 {
		return errors.New("pages cannot be negative")
	}
	return nil
}

func init() {
	models.Register[Publisher]()
	models.Register[Book]()
}

var bookResource = importexport.Resource[Book]{
	Configure: func(b *Book, config *importexport.Config) {
		config.Fields(
			importexport.F(&b.ISBN).Header("isbn"),
			importexport.F(&b.Title),
			importexport.F(&b.Pages),
			importexport.F(&b.Price),
			importexport.F(&b.Genre),
			importexport.F(&b.Publisher).Widget(importexport.ForeignKey(func(p *Publisher) *string { return &p.Name })),
			importexport.Computed("summary", func(ctx context.Context, b *Book) (string, error) {
				return fmt.Sprintf("%s (%d pages)", b.Title, b.Pages), nil
			}),
		)
		config.ImportIDFields(importexport.F(&b.ISBN))
		config.SkipUnchanged()
	},
}

func TestResourceCheck(t *testing.T) {
	if err := bookResource.Check(); err != nil {
		t.Fatal(err)
	}
	headers, err := bookResource.Headers()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"isbn", "title", "pages", "price", "genre", "publisher", "summary"}; !reflect.DeepEqual(headers, want) {
		t.Fatalf("headers = %v, want %v", headers, want)
	}

	var stray string
	broken := importexport.Resource[Book]{Configure: func(b *Book, config *importexport.Config) {
		config.Fields(importexport.F(&b.Title), importexport.F(&stray))
		config.ImportIDFields(importexport.F(&b.ISBN))
	}}
	err = broken.Check()
	if err == nil || !strings.Contains(err.Error(), "not a declared field") || !strings.Contains(err.Error(), "ImportIDFields") {
		t.Fatalf("expected configuration errors, got %v", err)
	}

	headers, err = importexport.Resource[Book]{}.Headers()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"id", "title", "isbn", "pages", "price", "genre", "publisher", "created_at", "updated_at"}; !reflect.DeepEqual(headers, want) {
		t.Fatalf("default headers = %v, want %v", headers, want)
	}
}

func TestFormatsRoundTrip(t *testing.T) {
	dataset := importexport.NewDataset("name", "quote, \"tricky\"", "empty")
	dataset.Append("Ada", "line\nbreak", "")
	dataset.Append("Grace", "tab\there", "")
	for _, format := range []importexport.Format{importexport.CSV, importexport.TSV, importexport.JSON} {
		var buffer bytes.Buffer
		if err := format.Write(&buffer, dataset); err != nil {
			t.Fatalf("%s: %v", format.Name(), err)
		}
		read, err := format.Read(&buffer)
		if err != nil {
			t.Fatalf("%s: %v", format.Name(), err)
		}
		if !reflect.DeepEqual(read, dataset) {
			t.Fatalf("%s round trip:\n got %#v\nwant %#v", format.Name(), read, dataset)
		}
	}

	read, err := importexport.JSON.Read(strings.NewReader(`[{"a": 1, "b": null, "c": true}, {"c": {"x": [1]}, "d": "text"}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := &importexport.Dataset{Headers: []string{"a", "b", "c", "d"}, Rows: [][]string{{"1", "", "true", ""}, {"", "", `{"x": [1]}`, "text"}}}
	if !reflect.DeepEqual(read, want) {
		t.Fatalf("json read:\n got %#v\nwant %#v", read, want)
	}

	read, err = importexport.CSV.Read(strings.NewReader("\xef\xbb\xbfname\nAda\n"))
	if err != nil || read.Headers[0] != "name" {
		t.Fatalf("byte order mark not stripped: %v %v", read, err)
	}
}

func setup(t *testing.T) context.Context {
	ctx, _ := testdb.New(t)
	testdb.CreateTables(t, ctx)
	publisher := &Publisher{Name: "Penguin"}
	if err := query.Save(ctx, publisher); err != nil {
		t.Fatal(err)
	}
	price := 12.5
	book := &Book{Title: "Dune", ISBN: "9780441013593", Pages: 412, Price: &price, Genre: "fiction", Publisher: models.RefTo(publisher)}
	if err := query.Save(ctx, book); err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestExport(t *testing.T) {
	ctx := setup(t)
	dataset, err := bookResource.Export(ctx, query.Objects[Book]())
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := importexport.CSV.Write(&buffer, dataset); err != nil {
		t.Fatal(err)
	}
	want := "isbn,title,pages,price,genre,publisher,summary\n9780441013593,Dune,412,12.5,fiction,Penguin,Dune (412 pages)\n"
	if buffer.String() != want {
		t.Fatalf("export:\n got %q\nwant %q", buffer.String(), want)
	}

	dataset, err = importexport.Resource[Book]{}.Export(ctx, query.Objects[Book]())
	if err != nil {
		t.Fatal(err)
	}
	if row := dataset.Row(0); row["publisher"] == "" || row["publisher"] == "Penguin" || row["created_at"] == "" {
		t.Fatalf("default export should use IDs and include timestamps: %v", row)
	}
}

func TestImport(t *testing.T) {
	ctx := setup(t)
	dataset, err := importexport.CSV.Read(strings.NewReader(strings.Join([]string{
		"isbn,title,pages,price,genre,publisher,summary",
		"9780441013593,Dune,412,12.5,fiction,Penguin,ignored",           // unchanged
		"9780441013593,Dune,500,12.5,fiction,Penguin,ignored",           // duplicate isbn: update
		"9780262033848,Introduction to Algorithms,1312,,science,,",      // new, no publisher
		"9780553380163,A Brief History of Time,256,18,science,Penguin,", // new
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}

	result, err := bookResource.Import(ctx, dataset, importexport.ImportOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Committed || result.Totals[importexport.RowSkip] != 1 || result.Totals[importexport.RowUpdate] != 1 || result.Totals[importexport.RowNew] != 2 {
		t.Fatalf("dry run result: %+v", result)
	}
	if count, _ := query.Objects[Book]().Count(ctx); count != 1 {
		t.Fatalf("dry run left %d books", count)
	}

	result, err = bookResource.Import(ctx, dataset, importexport.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Rows[2].Type != importexport.RowNew || result.Rows[2].ObjectRepr != "Introduction to Algorithms" {
		t.Fatalf("import result: %+v", result)
	}
	books, err := query.Objects[Book]().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 3 || books[0].Title != "A Brief History of Time" || books[0].Publisher.IsZero() || books[1].Pages != 500 || !books[2].Publisher.IsZero() || books[2].Price != nil {
		t.Fatalf("imported books: %+v %+v %+v", books[0], books[1], books[2])
	}
}

func TestImportRollsBackInvalidRows(t *testing.T) {
	ctx := setup(t)
	dataset := importexport.NewDataset("isbn", "title", "pages", "genre", "publisher")
	dataset.Append("1111111111111", "Good", "10", "fiction", "")
	dataset.Append("2222222222222", "Bad pages", "many", "poetry", "Nobody")
	dataset.Append("3333333333333", "Negative", "-1", "fiction", "")
	dataset.Append("1111111111111x", strings.Repeat("x", 300), "1", "fiction", "")

	result, err := bookResource.Import(ctx, dataset, importexport.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Committed || result.Totals[importexport.RowInvalid] != 3 || result.Totals[importexport.RowNew] != 1 {
		t.Fatalf("result: %+v", result)
	}
	invalid := result.Rows[1].ValidationErrors
	if invalid["pages"] == "" || invalid["genre"] == "" || !strings.Contains(invalid["publisher"], "does not exist") {
		t.Fatalf("validation errors: %v", invalid)
	}
	if result.Rows[2].ValidationErrors[""] != "pages cannot be negative" {
		t.Fatalf("model validation: %v", result.Rows[2].ValidationErrors)
	}
	if count, _ := query.Objects[Book]().Count(ctx); count != 1 {
		t.Fatalf("invalid import was not rolled back: %d books", count)
	}

	_, err = bookResource.Import(ctx, dataset, importexport.ImportOptions{RaiseErrors: true})
	var importError *importexport.ImportError
	if !errors.As(err, &importError) || importError.Row.Number != 2 || !strings.Contains(err.Error(), "row 2 is invalid") {
		t.Fatalf("RaiseErrors: %v", err)
	}
}

func TestImportDatabaseErrorsAreRowErrors(t *testing.T) {
	ctx := setup(t)
	resource := importexport.Resource[Book]{
		BeforeSaveInstance: func(ctx context.Context, book *Book, row importexport.Row, isNew bool) error {
			if book.Title == "Hooked" {
				return errors.New("refused by hook")
			}
			return nil
		},
	}
	dataset := importexport.NewDataset("title", "isbn", "pages", "genre")
	dataset.Append("Duplicate", "9780441013593", "1", "fiction")
	dataset.Append("Hooked", "4444444444444", "1", "fiction")
	dataset.Append("Fine", "5555555555555", "1", "fiction")

	result, err := resource.Import(ctx, dataset, importexport.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0].Type != importexport.RowError || result.Rows[1].Error == nil || result.Rows[1].Error.Error() != "refused by hook" {
		t.Fatalf("result: %+v", result.Rows)
	}
	if result.Rows[2].Type != importexport.RowNew || result.Committed {
		t.Fatalf("later rows should still be processed and everything rolled back: %+v", result)
	}
}
