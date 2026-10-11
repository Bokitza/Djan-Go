// Package importexport imports and exports model data as CSV, TSV or JSON, modeled on
// django-import-export. A Resource describes which fields map to which columns:
//
//	var PostResource = importexport.Resource[Post]{
//		Configure: func(p *Post, config *importexport.Config) {
//			config.Fields(
//				importexport.F(&p.ID),
//				importexport.F(&p.Title),
//				importexport.F(&p.Author).Widget(importexport.ForeignKey(func(a *Author) *string { return &a.Name })),
//				importexport.Computed("comments", func(ctx context.Context, p *Post) (string, error) { ... }),
//			)
//			config.ImportIDFields(importexport.F(&p.Slug))
//			config.SkipUnchanged()
//		},
//	}
//
//	dataset, err := PostResource.Export(ctx, query.Objects[Post]())
//	err = importexport.CSV.Write(writer, dataset)
//
//	dataset, err := importexport.CSV.Read(file)
//	result, err := PostResource.Import(ctx, dataset, importexport.ImportOptions{DryRun: true})
package importexport

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unsafe"

	"github.com/yoav-rogat/djan-go/models"
	"github.com/yoav-rogat/djan-go/query"
)

// Resource maps model T to dataset columns and back.
type Resource[T any] struct {
	// Name labels the resource when a user picks between several (defaults to "Default").
	Name string
	// Configure receives the model probe so fields are referenced with typed pointers.
	// Without it every field except binary ones is exported and imported, keyed by ID.
	Configure func(model *T, config *Config)
	// QuerySet limits the existing objects an import may find and update (defaults to all).
	QuerySet func(ctx context.Context) query.QuerySet[T]

	// BeforeImportRow runs before a row is processed and may modify it.
	BeforeImportRow func(ctx context.Context, row Row) error
	// SkipRow decides whether a row is skipped; original is the object before the row was
	// applied. It replaces the SkipUnchanged comparison.
	SkipRow func(ctx context.Context, object, original *T, row Row, isNew bool) bool
	// BeforeSaveInstance runs before an imported object is saved.
	BeforeSaveInstance func(ctx context.Context, object *T, row Row, isNew bool) error
	// AfterSaveInstance runs after an imported object was saved, in the same transaction.
	AfterSaveInstance func(ctx context.Context, object *T, row Row, isNew bool) error
}

// Field is a dataset column: a model field or a computed value.
type Field struct {
	pointer  unsafe.Pointer
	typ      reflect.Type
	header   string
	widget   Widget
	readonly bool
	compute  func(ctx context.Context, object any) (string, error)
	through  *Field
	err      error
}

// F references a field of the model: importexport.F(&p.Title).
func F[V any](field *V) Field {
	return Field{pointer: unsafe.Pointer(field), typ: reflect.TypeFor[V]()}
}

// Through is an export-only column with a field of a related model (author__name in
// django-import-export). The default header joins the lookup names with "__":
//
//	importexport.Through(&p.Author, func(a *Author) importexport.Field { return importexport.F(&a.Name) })
func Through[R any](foreignKey *models.FK[R], build func(related *R) Field) Field {
	field := Field{pointer: unsafe.Pointer(foreignKey), typ: reflect.TypeFor[models.FK[R]](), readonly: true}
	probe, err := models.ProbeOf[R]()
	if err != nil {
		field.err = err
		return field
	}
	inner := build(probe)
	field.through = &inner
	return field
}

// Computed is an export-only column whose value is computed from the object (dehydrate_<field>
// in django-import-export).
func Computed[T any](header string, compute func(ctx context.Context, object *T) (string, error)) Field {
	return Field{typ: reflect.TypeFor[T](), header: header, readonly: true, compute: func(ctx context.Context, object any) (string, error) {
		return compute(ctx, object.(*T))
	}}
}

// Header sets the column name (defaults to the field's lookup name, e.g. "author").
func (f Field) Header(name string) Field { f.header = name; return f }

// Widget sets how values are converted to and from cells.
func (f Field) Widget(widget Widget) Field { f.widget = widget; return f }

// ReadOnly exports the field but ignores it on import.
func (f Field) ReadOnly() Field { f.readonly = true; return f }

// Config collects resource options. It is filled by Resource.Configure.
type Config struct {
	fields         []Field
	exclude        []Field
	importIDFields []Field
	skipUnchanged  bool
}

// Fields sets the columns and their order.
func (c *Config) Fields(fields ...Field) { c.fields = fields }

// Exclude removes fields from the columns.
func (c *Config) Exclude(fields ...Field) { c.exclude = append(c.exclude, fields...) }

// ImportIDFields sets the fields used to find existing objects on import. It defaults to ID
// when the resource has an ID column; without one every imported row creates an object.
func (c *Config) ImportIDFields(fields ...Field) { c.importIDFields = fields }

// SkipUnchanged skips imported rows that would not change an existing object.
func (c *Config) SkipUnchanged() { c.skipUnchanged = true }

type column struct {
	header   string
	path     []*models.Field // foreign keys followed to reach field
	field    *models.Field   // nil for computed columns
	widget   Widget
	readonly bool
	compute  func(ctx context.Context, object any) (string, error)
}

func (c column) importable() bool { return c.field != nil && !c.readonly && !c.field.Auto }

// resolvePath resolves a field, following Through references, and returns the foreign keys on
// the way, the final field and the innermost Field (which may carry a header or widget).
func resolvePath(info *models.ModelInfo, field Field) ([]*models.Field, *models.Field, Field, error) {
	if field.err != nil {
		return nil, nil, field, field.err
	}
	resolved, ok := info.FieldAt(field.pointer, field.typ)
	if !ok {
		return nil, nil, field, fmt.Errorf("pointer of type *%s is not a declared field of %s", field.typ, info.Name)
	}
	if field.through == nil {
		return nil, resolved, field, nil
	}
	related := resolved.Related()
	if related == nil {
		return nil, nil, field, fmt.Errorf("target of %s is not registered", resolved)
	}
	path, final, innermost, err := resolvePath(related, *field.through)
	if err != nil {
		return nil, nil, field, err
	}
	return append([]*models.Field{resolved}, path...), final, innermost, nil
}

type resolvedResource struct {
	info           *models.ModelInfo
	columns        []column
	importIDFields []column
	skipUnchanged  bool
}

func (r Resource[T]) resolve() (*resolvedResource, error) {
	info, ok := models.InfoFor[T]()
	if !ok {
		return nil, fmt.Errorf("importexport: %s is not registered", reflect.TypeFor[T]())
	}
	config := &Config{}
	if r.Configure != nil {
		r.Configure((*T)(info.Probe()), config)
	}
	var problems []error
	resolveField := func(option string, field Field) *models.Field {
		resolved, ok := info.FieldAt(field.pointer, field.typ)
		if !ok {
			problems = append(problems, fmt.Errorf("%s: pointer of type *%s is not a declared field of %s", option, field.typ, info.Name))
		}
		return resolved
	}

	fields := config.fields
	if len(fields) == 0 {
		for _, field := range info.Fields {
			if field.Kind != models.KindBytes {
				fields = append(fields, Field{pointer: unsafe.Add(info.Probe(), field.Offset), typ: field.GoType})
			}
		}
	}
	excluded := map[*models.Field]bool{}
	for _, field := range config.exclude {
		excluded[resolveField("Exclude", field)] = true
	}
	resolved := &resolvedResource{info: info, skipUnchanged: config.skipUnchanged}
	headers := map[string]bool{}
	for _, field := range fields {
		column := column{header: field.header, widget: field.widget, readonly: field.readonly, compute: field.compute}
		if field.compute != nil {
			if field.typ != info.Type {
				problems = append(problems, fmt.Errorf("Computed %q: expects *%s, not *%s", field.header, field.typ, info.Type))
				continue
			}
		} else {
			path, final, innermost, err := resolvePath(info, field)
			if err != nil {
				problems = append(problems, fmt.Errorf("Fields: %w", err))
				continue
			}
			if len(path) == 0 && excluded[final] {
				continue
			}
			column.path, column.field = path, final
			if column.header == "" {
				column.header = innermost.header
			}
			if column.header == "" {
				names := make([]string, 0, len(path)+1)
				for _, foreignKey := range path {
					names = append(names, foreignKey.LookupName())
				}
				column.header = strings.Join(append(names, final.LookupName()), "__")
			}
			if column.widget == nil {
				column.widget = innermost.widget
			}
			if column.widget == nil {
				column.widget = fieldWidget{final}
			}
		}
		if checked, ok := column.widget.(interface{ check() error }); ok && checked.check() != nil {
			problems = append(problems, fmt.Errorf("%s: %w", column.header, checked.check()))
		}
		if headers[column.header] {
			problems = append(problems, fmt.Errorf("Fields: duplicate column %q", column.header))
		}
		headers[column.header] = true
		resolved.columns = append(resolved.columns, column)
	}

	importIDFields := config.importIDFields
	if len(importIDFields) == 0 {
		for _, candidate := range resolved.columns {
			if candidate.field == info.PK && len(candidate.path) == 0 {
				resolved.importIDFields = []column{candidate}
			}
		}
	}
	for _, field := range importIDFields {
		target := resolveField("ImportIDFields", field)
		if target == nil {
			continue
		}
		index := -1
		for position, column := range resolved.columns {
			if column.field == target && len(column.path) == 0 {
				index = position
			}
		}
		if index < 0 {
			problems = append(problems, fmt.Errorf("ImportIDFields: %s is not one of the resource fields", target.Name))
			continue
		}
		resolved.importIDFields = append(resolved.importIDFields, resolved.columns[index])
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("importexport: invalid %s resource: %w", info.Name, errors.Join(problems...))
	}
	return resolved, nil
}

// Check reports configuration problems; call it from a test or at startup.
func (r Resource[T]) Check() error {
	_, err := r.resolve()
	return err
}

// Headers returns the column names, e.g. for an import template.
func (r Resource[T]) Headers() ([]string, error) {
	resolved, err := r.resolve()
	if err != nil {
		return nil, err
	}
	headers := make([]string, len(resolved.columns))
	for index, column := range resolved.columns {
		headers[index] = column.header
	}
	return headers, nil
}

// ImportHeaders returns the columns that are read on import; the others are export-only.
func (r Resource[T]) ImportHeaders() ([]string, error) {
	resolved, err := r.resolve()
	if err != nil {
		return nil, err
	}
	var headers []string
	for _, column := range resolved.columns {
		if column.importable() {
			headers = append(headers, column.header)
		}
	}
	for _, column := range resolved.importIDFields {
		if !slices.Contains(headers, column.header) {
			headers = append(headers, column.header)
		}
	}
	return headers, nil
}

// Export renders every object of the queryset as a dataset row.
func (r Resource[T]) Export(ctx context.Context, queryset query.QuerySet[T]) (*Dataset, error) {
	resolved, err := r.resolve()
	if err != nil {
		return nil, err
	}
	objects, err := queryset.Dynamic().All(ctx)
	if err != nil {
		return nil, err
	}
	dataset := &Dataset{}
	targets := make([][]any, len(resolved.columns))
	for index, column := range resolved.columns {
		dataset.Headers = append(dataset.Headers, column.header)
		if column.compute != nil {
			continue
		}
		if targets[index], err = followPath(ctx, objects, column.path); err != nil {
			return nil, err
		}
		if _, loads := column.widget.(interface{ loadsRelated() }); loads {
			if err := query.PreloadRelated(ctx, compact(targets[index]), column.field); err != nil {
				return nil, err
			}
		}
	}
	for row, object := range objects {
		cells := make([]string, len(resolved.columns))
		for index, column := range resolved.columns {
			var cell string
			var err error
			switch {
			case column.compute != nil:
				cell, err = column.compute(ctx, object)
			case targets[index][row] != nil:
				cell, err = column.widget.Render(ctx, column.field.Addr(targets[index][row]).Interface())
			}
			if err != nil {
				return nil, fmt.Errorf("importexport: exporting %s #%d, column %q: %w", resolved.info.Name, resolved.info.PKOf(object), column.header, err)
			}
			cells[index] = cell
		}
		dataset.Rows = append(dataset.Rows, cells)
	}
	return dataset, nil
}

// followPath returns, for each object, the object reached by following the foreign keys
// (nil where a key is empty), loading each level with one query.
func followPath(ctx context.Context, objects []any, path []*models.Field) ([]any, error) {
	current := objects
	for _, foreignKey := range path {
		if err := query.PreloadRelated(ctx, compact(current), foreignKey); err != nil {
			return nil, err
		}
		next := make([]any, len(current))
		for index, object := range current {
			if object == nil {
				continue
			}
			cached := foreignKey.Addr(object).MethodByName("Cached").Call(nil)[0]
			if !cached.IsNil() {
				next[index] = cached.Interface()
			}
		}
		current = next
	}
	return current, nil
}

func compact(objects []any) []any {
	present := make([]any, 0, len(objects))
	for _, object := range objects {
		if object != nil {
			present = append(present, object)
		}
	}
	return present
}
