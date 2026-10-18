package models

import (
	"fmt"
	"reflect"
	"runtime"
	"time"
	"unsafe"

	"github.com/shopspring/decimal"
)

// Schema collects the column declarations of one model. It is passed to the model's Schema method.
type Schema struct {
	info           *ModelInfo
	base           unsafe.Pointer
	size           uintptr
	candidates     map[slot]candidate
	ignored        map[string]bool
	errors         []error
	ordering       []OrderTerm
	orderingAt     string
	uniqueTogether [][]any
	indexes        [][]any
	compositeAt    []string
}

type slot struct {
	offset uintptr
	typ    reflect.Type
}

type candidate struct {
	index []int
	name  string
}

// OrderTerm is a typed ordering term created with Asc or Desc.
type OrderTerm struct {
	Pointer    unsafe.Pointer
	Type       reflect.Type
	Descending bool
}

// Asc orders by a field ascending.
func Asc[V any](field *V) OrderTerm {
	return OrderTerm{Pointer: unsafe.Pointer(field), Type: reflect.TypeFor[V]()}
}

// Desc orders by a field descending.
func Desc[V any](field *V) OrderTerm {
	return OrderTerm{Pointer: unsafe.Pointer(field), Type: reflect.TypeFor[V](), Descending: true}
}

// OrderField is a resolved ordering term.
type OrderField struct {
	Field      *Field
	Descending bool
}

func callerLocation(skip int) string {
	_, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return "unknown location"
	}
	return fmt.Sprintf("%s:%d", file, line)
}

func (s *Schema) errorf(location, format string, arguments ...any) {
	s.errors = append(s.errors, fmt.Errorf("%s: %s", location, fmt.Sprintf(format, arguments...)))
}

// Table overrides the table name (default: <app>_<model in snake_case>).
func (s *Schema) Table(name string) { s.info.Table = name }

// App overrides the app label (default: the Go package name).
func (s *Schema) App(label string) { s.info.App = label }

// Verbose sets human readable singular and plural names.
func (s *Schema) Verbose(singular, plural string) {
	s.info.VerboseName = singular
	s.info.VerboseNamePlural = plural
}

// Ordering sets the default ordering of querysets.
func (s *Schema) Ordering(terms ...OrderTerm) {
	s.ordering = terms
	s.orderingAt = callerLocation(1)
}

// Ignore marks a struct field as not stored in the database.
func (s *Schema) Ignore(fields ...any) {
	location := callerLocation(1)
	for _, field := range fields {
		value := reflect.ValueOf(field)
		if value.Kind() != reflect.Pointer {
			s.errorf(location, "Ignore expects pointers to fields, got %T", field)
			continue
		}
		found, ok := s.candidates[slot{uintptr(value.UnsafePointer()) - uintptr(s.base), value.Type().Elem()}]
		if !ok {
			s.errorf(location, "Ignore: pointer is not a field of %s", s.info.Name)
			continue
		}
		s.ignored[found.name] = true
	}
}

// UniqueTogether adds a composite unique constraint over the given field pointers.
func (s *Schema) UniqueTogether(fields ...any) {
	s.uniqueTogether = append(s.uniqueTogether, fields)
	s.compositeAt = append(s.compositeAt, callerLocation(1))
}

// CompositeIndex adds a multi-column index over the given field pointers.
func (s *Schema) CompositeIndex(fields ...any) {
	s.indexes = append(s.indexes, fields)
}

func (s *Schema) declare(location string, pointer unsafe.Pointer, typ reflect.Type, kind Kind) *Field {
	offset := uintptr(pointer) - uintptr(s.base)
	if uintptr(pointer) < uintptr(s.base) || offset >= s.size {
		s.errorf(location, "%s field: pointer is not a field of %s (use the receiver, e.g. &p.Title)", kind, s.info.Name)
		return &Field{Model: s.info}
	}
	found, ok := s.candidates[slot{offset, typ}]
	if !ok {
		s.errorf(location, "%s field: pointer does not refer to a %s field of %s", kind, typ, s.info.Name)
		return &Field{Model: s.info}
	}
	if existing := s.info.fieldByGoName(found.name); existing != nil {
		s.errorf(location, "field %s is declared twice (first at %s)", found.name, existing.DeclaredAt)
		return &Field{Model: s.info}
	}
	goType := s.info.Type.FieldByIndex(found.index).Type
	field := &Field{
		Model:       s.info,
		Name:        found.name,
		Column:      SnakeCase(found.name),
		Kind:        kind,
		GoType:      goType,
		Index:       found.index,
		Offset:      offset,
		Nullable:    goType.Kind() == reflect.Pointer,
		Editable:    true,
		VerboseName: Humanize(found.name),
		DeclaredAt:  location,
	}
	if kind == KindForeignKey {
		field.Column = SnakeCase(found.name) + "_id"
		field.Indexed = true
		field.OnDelete = Cascade
	}
	s.info.Fields = append(s.info.Fields, field)
	return field
}

// Column configures a declared field. V is the field's value type, so Default and Choices are type-checked.
type Column[V any] struct{ field *Field }

func newColumn[V any](s *Schema, location string, pointer unsafe.Pointer, typ reflect.Type, kind Kind) *Column[V] {
	return &Column[V]{field: s.declare(location, pointer, typ, kind)}
}

// Default sets the value used by models.New and the migration DEFAULT clause.
func (c *Column[V]) Default(value V) *Column[V] {
	c.field.HasDefault = true
	c.field.Default = value
	return c
}

// Choices restricts the allowed values; the admin renders a select.
func (c *Column[V]) Choices(values ...V) *Column[V] {
	c.field.Choices = c.field.Choices[:0]
	for _, value := range values {
		c.field.Choices = append(c.field.Choices, value)
	}
	return c
}

// Choice is a choice value with its display label; create it with Option.
type Choice[V any] struct {
	Value V
	Label string
}

// Option pairs a choice value with its display label: models.Option("Open", "פתוח").
func Option[V any](value V, label string) Choice[V] { return Choice[V]{Value: value, Label: label} }

// LabeledChoices restricts the allowed values like Choices and shows each with its label.
func (c *Column[V]) LabeledChoices(choices ...Choice[V]) *Column[V] {
	c.field.Choices = c.field.Choices[:0]
	c.field.ChoiceLabels = map[string]string{}
	for _, choice := range choices {
		c.field.Choices = append(c.field.Choices, choice.Value)
		c.field.ChoiceLabels[fmt.Sprint(choice.Value)] = choice.Label
	}
	return c
}

// Unique adds a unique constraint.
func (c *Column[V]) Unique() *Column[V] { c.field.Unique = true; return c }

// Index adds a database index.
func (c *Column[V]) Index() *Column[V] { c.field.Indexed = true; return c }

// Blank allows empty values in admin forms.
func (c *Column[V]) Blank() *Column[V] { c.field.Blank = true; return c }

// ReadOnly makes the field read-only in the admin.
func (c *Column[V]) ReadOnly() *Column[V] { c.field.Editable = false; return c }

// Verbose sets the human readable name.
func (c *Column[V]) Verbose(name string) *Column[V] { c.field.VerboseName = name; return c }

// Help sets the help text shown under admin form inputs.
func (c *Column[V]) Help(text string) *Column[V] { c.field.HelpText = text; return c }

// DBColumn overrides the column name.
func (c *Column[V]) DBColumn(name string) *Column[V] { c.field.Column = name; return c }

// Field returns the declared field.
func (c *Column[V]) Field() *Field { return c.field }

// Char declares a varchar column with a maximum length.
func (s *Schema) Char(field *string, maxLength int) *Column[string] {
	column := newColumn[string](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[string](), KindChar)
	column.field.MaxLength = maxLength
	return column
}

// NullableChar declares a nullable varchar column.
func (s *Schema) NullableChar(field **string, maxLength int) *Column[string] {
	column := newColumn[string](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*string](), KindChar)
	column.field.MaxLength = maxLength
	return column
}

// Text declares a text column.
func (s *Schema) Text(field *string) *Column[string] {
	return newColumn[string](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[string](), KindText)
}

// NullableText declares a nullable text column.
func (s *Schema) NullableText(field **string) *Column[string] {
	return newColumn[string](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*string](), KindText)
}

// Int declares a bigint column backed by a Go int.
func (s *Schema) Int(field *int) *Column[int] {
	return newColumn[int](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[int](), KindInt)
}

// NullableInt declares a nullable bigint column backed by *int.
func (s *Schema) NullableInt(field **int) *Column[int] {
	return newColumn[int](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*int](), KindInt)
}

// Int32 declares an integer column.
func (s *Schema) Int32(field *int32) *Column[int32] {
	return newColumn[int32](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[int32](), KindInt32)
}

// NullableInt32 declares a nullable integer column.
func (s *Schema) NullableInt32(field **int32) *Column[int32] {
	return newColumn[int32](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*int32](), KindInt32)
}

// Int64 declares a bigint column.
func (s *Schema) Int64(field *int64) *Column[int64] {
	return newColumn[int64](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[int64](), KindInt64)
}

// NullableInt64 declares a nullable bigint column.
func (s *Schema) NullableInt64(field **int64) *Column[int64] {
	return newColumn[int64](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*int64](), KindInt64)
}

// Float declares a double precision column.
func (s *Schema) Float(field *float64) *Column[float64] {
	return newColumn[float64](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[float64](), KindFloat)
}

// NullableFloat declares a nullable double precision column.
func (s *Schema) NullableFloat(field **float64) *Column[float64] {
	return newColumn[float64](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*float64](), KindFloat)
}

// Bool declares a boolean column.
func (s *Schema) Bool(field *bool) *Column[bool] {
	return newColumn[bool](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[bool](), KindBool)
}

// NullableBool declares a nullable boolean column.
func (s *Schema) NullableBool(field **bool) *Column[bool] {
	return newColumn[bool](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*bool](), KindBool)
}

// DateTime declares a timestamptz column.
func (s *Schema) DateTime(field *time.Time) *Column[time.Time] {
	return newColumn[time.Time](s, callerLocation(1), unsafe.Pointer(field), timeType, KindDateTime)
}

// NullableDateTime declares a nullable timestamptz column.
func (s *Schema) NullableDateTime(field **time.Time) *Column[time.Time] {
	return newColumn[time.Time](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*time.Time](), KindDateTime)
}

// Date declares a date column (the time of day is ignored).
func (s *Schema) Date(field *time.Time) *Column[time.Time] {
	return newColumn[time.Time](s, callerLocation(1), unsafe.Pointer(field), timeType, KindDate)
}

// NullableDate declares a nullable date column.
func (s *Schema) NullableDate(field **time.Time) *Column[time.Time] {
	return newColumn[time.Time](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*time.Time](), KindDate)
}

// Decimal declares a numeric(maxDigits, places) column, e.g. money as Decimal(&t.Amount, 12, 2).
func (s *Schema) Decimal(field *decimal.Decimal, maxDigits, places int) *Column[decimal.Decimal] {
	return newDecimal(s, callerLocation(1), unsafe.Pointer(field), decimalType, maxDigits, places)
}

// NullableDecimal declares a nullable numeric column.
func (s *Schema) NullableDecimal(field **decimal.Decimal, maxDigits, places int) *Column[decimal.Decimal] {
	return newDecimal(s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*decimal.Decimal](), maxDigits, places)
}

func newDecimal(s *Schema, location string, pointer unsafe.Pointer, typ reflect.Type, maxDigits, places int) *Column[decimal.Decimal] {
	column := newColumn[decimal.Decimal](s, location, pointer, typ, KindDecimal)
	if maxDigits <= 0 || places < 0 || places > maxDigits {
		s.errorf(location, "decimal field %s: need 0 <= places <= maxDigits and maxDigits > 0", column.field.Name)
	}
	column.field.MaxDigits, column.field.Places = maxDigits, places
	return column
}

// Bytes declares a bytea column.
func (s *Schema) Bytes(field *[]byte) *Column[[]byte] {
	return newColumn[[]byte](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[[]byte](), KindBytes)
}

// UUID declares a uuid column for any [16]byte based type (such as github.com/google/uuid.UUID).
func UUID[V ~[16]byte](s *Schema, field *V) *Column[V] {
	return newColumn[V](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[V](), KindUUID)
}

// NullableUUID declares a nullable uuid column.
func NullableUUID[V ~[16]byte](s *Schema, field **V) *Column[V] {
	return newColumn[V](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[*V](), KindUUID)
}

// JSON declares a jsonb column holding any JSON-serializable value.
func JSON[V any](s *Schema, field *V) *Column[V] {
	return newColumn[V](s, callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[V](), KindJSON)
}

// ForeignKeyColumn configures a foreign key.
type ForeignKeyColumn struct{ field *Field }

// ForeignKey declares a foreign key column (<name>_id) referencing model T.
func ForeignKey[T any](s *Schema, field *FK[T]) *ForeignKeyColumn {
	declared := s.declare(callerLocation(1), unsafe.Pointer(field), reflect.TypeFor[FK[T]](), KindForeignKey)
	declared.RelatedType = reflect.TypeFor[T]()
	return &ForeignKeyColumn{field: declared}
}

// OnDelete sets the referential action (default Cascade).
func (c *ForeignKeyColumn) OnDelete(action OnDelete) *ForeignKeyColumn {
	c.field.OnDelete = action
	return c
}

// Nullable allows NULL; an FK with ID 0 is stored as NULL.
func (c *ForeignKeyColumn) Nullable() *ForeignKeyColumn {
	c.field.Nullable = true
	c.field.Blank = true
	return c
}

// NoIndex removes the default index on the column.
func (c *ForeignKeyColumn) NoIndex() *ForeignKeyColumn { c.field.Indexed = false; return c }

// Unique makes the relation one-to-one.
func (c *ForeignKeyColumn) Unique() *ForeignKeyColumn { c.field.Unique = true; return c }

// ReadOnly makes the field read-only in the admin.
func (c *ForeignKeyColumn) ReadOnly() *ForeignKeyColumn { c.field.Editable = false; return c }

// Verbose sets the human readable name.
func (c *ForeignKeyColumn) Verbose(name string) *ForeignKeyColumn {
	c.field.VerboseName = name
	return c
}

// Help sets the help text shown in admin forms.
func (c *ForeignKeyColumn) Help(text string) *ForeignKeyColumn { c.field.HelpText = text; return c }

// DBColumn overrides the column name.
func (c *ForeignKeyColumn) DBColumn(name string) *ForeignKeyColumn { c.field.Column = name; return c }

// InLabel loads the related object before the model's String method is called to render a label.
func (c *ForeignKeyColumn) InLabel() *ForeignKeyColumn { c.field.InLabel = true; return c }

// Field returns the declared field.
func (c *ForeignKeyColumn) Field() *Field { return c.field }
