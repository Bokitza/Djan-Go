// Package admin is a server-rendered admin site (HTMX + Tailwind + Alpine.js) for registered
// models. Row-level CRUD rules are expressed in Go as querysets.
package admin

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"unsafe"

	"github.com/Bokitza/djan-go/auth"
	"github.com/Bokitza/djan-go/models"
	"github.com/Bokitza/djan-go/query"
)

// Request is the admin request with the logged in staff user.
type Request struct {
	*http.Request
	User *auth.User
	Site *Site
}

// RuleFunc narrows a queryset to the rows a request may act on.
type RuleFunc[T any] func(r *Request, qs query.QuerySet[T]) query.QuerySet[T]

// ModelAdmin configures how model T appears in the admin and which rows each action may touch.
type ModelAdmin[T any] struct {
	// Configure receives the model probe so fields are referenced with typed pointers.
	Configure func(model *T, config *Config)
	// Icon is the sidebar icon name (see icons.go), e.g. "file-text".
	Icon string

	// ViewQuerySet limits the rows that are listed and visible.
	ViewQuerySet RuleFunc[T]
	// ChangeQuerySet limits the visible rows that may be edited.
	ChangeQuerySet RuleFunc[T]
	// DeleteQuerySet limits the visible rows that may be deleted.
	DeleteQuerySet RuleFunc[T]
	// CanAdd decides whether the request may create objects (in addition to the add permission).
	CanAdd func(r *Request) bool
	// Permissions overrides what the model permissions grant, e.g. to let every member view a model.
	// Row rules (the QuerySet functions) still apply on top of it.
	Permissions func(r *Request, granted Permissions) Permissions

	// BeforeSave runs after form validation and before the object is saved.
	BeforeSave func(r *Request, object *T, isNew bool) error
	// AfterSave runs after the object was saved, in the same transaction.
	AfterSave func(r *Request, object *T, isNew bool) error

	// ImportExport enables importing and exporting data; nil disables both.
	ImportExport *ImportExport[T]
	// ManyToMany adds checkbox editors for links stored in join models (see admin.ManyToMany).
	ManyToMany []ManyToManyField[T]

	extras []extraField
}

// Field is a typed reference to a model field (optionally through foreign keys).
type Field struct {
	pointer unsafe.Pointer
	typ     reflect.Type
	through *Field
	label   string
}

// F references a field of the model: admin.F(&p.Title).
func F[V any](field *V) Field {
	return Field{pointer: unsafe.Pointer(field), typ: reflect.TypeFor[V]()}
}

// Through references a field of a related model:
//
//	admin.Through(&p.Author, func(a *Author) admin.Field { return admin.F(&a.Name) })
func Through[R any](foreignKey *models.FK[R], build func(related *R) Field) Field {
	probe, err := models.ProbeOf[R]()
	if err != nil {
		return Field{pointer: unsafe.Pointer(foreignKey), typ: reflect.TypeFor[models.FK[R]](), label: "!" + err.Error()}
	}
	inner := build(probe)
	return Field{pointer: unsafe.Pointer(foreignKey), typ: reflect.TypeFor[models.FK[R]](), through: &inner}
}

// Labeled overrides the column header.
func (f Field) Labeled(label string) Field { f.label = label; return f }

// Config collects list and form options. It is filled by ModelAdmin.Configure.
type Config struct {
	listDisplay  []Field
	searchFields []Field
	listFilter   []Field
	readonly     []Field
	readonlyWhen []conditionalReadonly
	exclude      []Field
	perPage      int
	ordering     []models.OrderTerm
}

type conditionalReadonly struct {
	condition func(r *Request, object any) bool
	fields    []Field
}

// ReadonlyWhen makes fields read-only on the form while condition holds. It is checked against
// the object as stored, before the submitted form is applied.
func ReadonlyWhen[T any](config *Config, condition func(r *Request, object *T) bool, fields ...Field) {
	config.readonlyWhen = append(config.readonlyWhen, conditionalReadonly{
		condition: func(r *Request, object any) bool { return condition(r, object.(*T)) },
		fields:    fields,
	})
}

// ListDisplay sets the list columns. The first column links to the change page.
func (c *Config) ListDisplay(fields ...Field) { c.listDisplay = fields }

// SearchFields enables the search box over text fields.
func (c *Config) SearchFields(fields ...*string) {
	for _, field := range fields {
		c.searchFields = append(c.searchFields, F(field))
	}
}

// ListFilter adds sidebar filters (booleans, choices, foreign keys, dates and plain values).
func (c *Config) ListFilter(fields ...Field) { c.listFilter = fields }

// Readonly shows fields in the form without allowing edits.
func (c *Config) Readonly(fields ...Field) { c.readonly = append(c.readonly, fields...) }

// Exclude hides fields from the form.
func (c *Config) Exclude(fields ...Field) { c.exclude = append(c.exclude, fields...) }

// PerPage sets the list page size (default 25).
func (c *Config) PerPage(count int) { c.perPage = count }

// Ordering sets the default list ordering (defaults to the model ordering).
func (c *Config) Ordering(terms ...models.OrderTerm) { c.ordering = terms }

// column is a resolved list column.
type column struct {
	label string
	path  []*models.Field // foreign keys to follow
	field *models.Field   // nil means the object label
}

func (c column) sortKey() string {
	if len(c.path) > 0 || c.field == nil {
		return ""
	}
	return c.field.LookupName()
}

type resolvedConfig struct {
	columns      []column
	searchFields []*models.Field
	filters      []*models.Field
	readonly     map[*models.Field]bool
	readonlyWhen []resolvedReadonly
	exclude      map[*models.Field]bool
	perPage      int
	ordering     []string
}

func resolveField(info *models.ModelInfo, field Field) ([]*models.Field, *models.Field, error) {
	if len(field.label) > 0 && field.label[0] == '!' {
		return nil, nil, fmt.Errorf("%s", field.label[1:])
	}
	resolved, ok := info.FieldAt(field.pointer, field.typ)
	if !ok {
		return nil, nil, fmt.Errorf("pointer of type *%s is not a declared field of %s", field.typ, info.Name)
	}
	if field.through == nil {
		return nil, resolved, nil
	}
	related := resolved.Related()
	if related == nil {
		return nil, nil, fmt.Errorf("target of %s is not registered", resolved)
	}
	path, final, err := resolveField(related, *field.through)
	if err != nil {
		return nil, nil, err
	}
	return append([]*models.Field{resolved}, path...), final, nil
}

func resolveConfig(info *models.ModelInfo, config *Config) (*resolvedConfig, []error) {
	var problems []error
	resolved := &resolvedConfig{readonly: map[*models.Field]bool{}, exclude: map[*models.Field]bool{}, perPage: config.perPage}
	if resolved.perPage <= 0 {
		resolved.perPage = 25
	}
	local := func(kind string, field Field) *models.Field {
		path, final, err := resolveField(info, field)
		if err == nil && len(path) > 0 {
			err = fmt.Errorf("%s cannot use admin.Through", kind)
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", kind, err))
			return nil
		}
		return final
	}
	for _, field := range config.listDisplay {
		path, final, err := resolveField(info, field)
		if err != nil {
			problems = append(problems, fmt.Errorf("ListDisplay: %w", err))
			continue
		}
		label := field.label
		if label == "" {
			label = final.VerboseName
			if len(path) > 0 {
				label = path[len(path)-1].VerboseName + " " + lowerFirst(final.VerboseName)
			}
		}
		resolved.columns = append(resolved.columns, column{label: label, path: path, field: final})
	}
	if len(resolved.columns) == 0 {
		resolved.columns = defaultColumns(info)
	}
	for _, field := range config.searchFields {
		if final := local("SearchFields", field); final != nil {
			resolved.searchFields = append(resolved.searchFields, final)
		}
	}
	for _, field := range config.listFilter {
		if final := local("ListFilter", field); final != nil {
			if final.Kind == models.KindJSON || final.Kind == models.KindBytes || final.Kind == models.KindText {
				problems = append(problems, fmt.Errorf("ListFilter: %s fields cannot be filtered (%s)", final.Kind, final))
				continue
			}
			resolved.filters = append(resolved.filters, final)
		}
	}
	for _, field := range config.readonly {
		if final := local("Readonly", field); final != nil {
			resolved.readonly[final] = true
		}
	}
	for _, conditional := range config.readonlyWhen {
		resolvedConditional := resolvedReadonly{condition: conditional.condition, fields: map[*models.Field]bool{}}
		for _, field := range conditional.fields {
			if final := local("ReadonlyWhen", field); final != nil {
				resolvedConditional.fields[final] = true
			}
		}
		resolved.readonlyWhen = append(resolved.readonlyWhen, resolvedConditional)
	}
	for _, field := range config.exclude {
		if final := local("Exclude", field); final != nil {
			resolved.exclude[final] = true
		}
	}
	for _, term := range config.ordering {
		field, ok := info.FieldAt(term.Pointer, term.Type)
		if !ok {
			problems = append(problems, fmt.Errorf("Ordering: pointer is not a declared field of %s", info.Name))
			continue
		}
		name := field.LookupName()
		if term.Descending {
			name = "-" + name
		}
		resolved.ordering = append(resolved.ordering, name)
	}
	return resolved, problems
}

func defaultColumns(info *models.ModelInfo) []column {
	columns := []column{{label: info.VerboseName}}
	for _, field := range info.Fields {
		if len(columns) >= 4 {
			break
		}
		if field.Auto || field.Kind == models.KindText || field.Kind == models.KindJSON || field.Kind == models.KindBytes || field.Editable == false {
			continue
		}
		columns = append(columns, column{label: field.VerboseName, field: field})
	}
	return columns
}

func lowerFirst(value string) string {
	if value == "" {
		return value
	}
	return strings.ToLower(value[:1]) + value[1:]
}

type resolvedReadonly struct {
	condition func(r *Request, object any) bool
	fields    map[*models.Field]bool
}

// lockedFields returns the fields made read-only by ReadonlyWhen for this request and object.
func (c *resolvedConfig) lockedFields(r *Request, object any) map[*models.Field]bool {
	locked := map[*models.Field]bool{}
	for _, conditional := range c.readonlyWhen {
		if conditional.condition(r, object) {
			for field := range conditional.fields {
				locked[field] = true
			}
		}
	}
	return locked
}
