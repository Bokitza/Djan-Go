package models

import (
	"fmt"
	"reflect"
	"time"
	"unsafe"
)

// Kind is the logical type of a field.
type Kind int

const (
	KindAutoID Kind = iota
	KindChar
	KindText
	KindInt
	KindInt32
	KindInt64
	KindFloat
	KindBool
	KindDateTime
	KindBytes
	KindUUID
	KindJSON
	KindForeignKey
)

var kindNames = map[Kind]string{
	KindAutoID: "auto id", KindChar: "char", KindText: "text", KindInt: "int", KindInt32: "int32",
	KindInt64: "int64", KindFloat: "float", KindBool: "bool", KindDateTime: "datetime",
	KindBytes: "bytes", KindUUID: "uuid", KindJSON: "json", KindForeignKey: "foreign key",
}

func (k Kind) String() string { return kindNames[k] }

// IsText reports whether text lookups (contains, startswith, ...) apply to the kind.
func (k Kind) IsText() bool { return k == KindChar || k == KindText }

// IsNumeric reports whether the kind is a number.
func (k Kind) IsNumeric() bool {
	return k == KindInt || k == KindInt32 || k == KindInt64 || k == KindFloat || k == KindAutoID
}

// OnDelete is the referential action of a foreign key.
type OnDelete string

const (
	Cascade  OnDelete = "CASCADE"
	Protect  OnDelete = "RESTRICT"
	SetNull  OnDelete = "SET NULL"
	NoAction OnDelete = "NO ACTION"
)

// Field describes one column of a model.
type Field struct {
	Model       *ModelInfo
	Name        string // Go field path, e.g. "Title" or "Author"
	Column      string
	Kind        Kind
	GoType      reflect.Type // type of the struct field
	Index       []int
	Offset      uintptr
	Nullable    bool
	PrimaryKey  bool
	Auto        bool // managed by the database: id, created_at, updated_at
	MaxLength   int
	Unique      bool
	Indexed     bool
	Blank       bool
	Editable    bool
	HasDefault  bool
	Default     any // a value of the field's base (non-pointer) type
	Choices     []any
	VerboseName string
	HelpText    string
	RelatedType reflect.Type // FK target
	OnDelete    OnDelete
	DeclaredAt  string
}

func (f *Field) String() string { return f.Model.Name + "." + f.Name }

// LookupName is the snake_case name used by the string query API and URLs.
func (f *Field) LookupName() string {
	if f.Kind == KindForeignKey {
		return trimSuffix(f.Column, "_id")
	}
	return f.Column
}

func trimSuffix(value, suffix string) string {
	if len(value) > len(suffix) && value[len(value)-len(suffix):] == suffix {
		return value[:len(value)-len(suffix)]
	}
	return value
}

// Related returns the target model of a foreign key, or nil.
func (f *Field) Related() *ModelInfo {
	if f.RelatedType == nil {
		return nil
	}
	info, _ := infoForType(f.RelatedType)
	return info
}

// IsPointer reports whether the Go field is a pointer (a nullable column).
func (f *Field) IsPointer() bool { return f.GoType.Kind() == reflect.Pointer }

// BaseType is the Go type of the value without the nullable pointer.
func (f *Field) BaseType() reflect.Type {
	if f.IsPointer() {
		return f.GoType.Elem()
	}
	return f.GoType
}

// Addr returns the addressable struct field inside object (a pointer to the model).
func (f *Field) Addr(object any) reflect.Value {
	value := reflect.ValueOf(object)
	if value.Kind() != reflect.Pointer || value.Elem().Type() != f.Model.Type {
		panic(fmt.Sprintf("models: %s expects *%s, got %T", f, f.Model.Type, object))
	}
	return reflect.NewAt(f.GoType, unsafe.Add(value.UnsafePointer(), f.Offset)).Elem()
}

// Get returns the database value of the field: NULL is nil, foreign keys are IDs.
func (f *Field) Get(object any) any {
	return f.ToDatabase(f.Addr(object).Interface())
}

// ToDatabase converts a Go value of this field (or of its base type) to a value pgx can encode.
func (f *Field) ToDatabase(value any) any {
	if value == nil {
		return nil
	}
	if key, ok := value.(foreignKey); ok {
		if key.ForeignKeyID() == 0 && f.Nullable {
			return nil
		}
		return key.ForeignKeyID()
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return nil
		}
		reflected = reflected.Elem()
	}
	return normalizeValue(reflected)
}

func normalizeValue(value reflect.Value) any {
	switch value.Kind() {
	case reflect.String:
		return value.String()
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(value.Uint())
	case reflect.Float32, reflect.Float64:
		return value.Float()
	case reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 && value.Len() == 16 {
			var uuid [16]byte
			reflect.Copy(reflect.ValueOf(&uuid).Elem(), value)
			return uuid
		}
	}
	return value.Interface()
}

// Set assigns a Go value (of the field type, its base type, or nil) to the field of object.
func (f *Field) Set(object any, value any) error {
	target := f.Addr(object)
	if f.Kind == KindForeignKey {
		id, err := toInt64(value)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		target.FieldByName("ID").SetInt(id)
		return nil
	}
	if value == nil {
		target.Set(reflect.Zero(f.GoType))
		return nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Type() == f.GoType {
		target.Set(reflected)
		return nil
	}
	base := f.BaseType()
	if !reflected.Type().ConvertibleTo(base) {
		return fmt.Errorf("%s: cannot assign %T", f, value)
	}
	converted := reflected.Convert(base)
	if f.IsPointer() {
		pointer := reflect.New(base)
		pointer.Elem().Set(converted)
		target.Set(pointer)
		return nil
	}
	target.Set(converted)
	return nil
}

func toInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case foreignKey:
		return typed.ForeignKeyID(), nil
	}
	return 0, fmt.Errorf("cannot use %T as a foreign key id", value)
}

// ScanTarget returns a destination for pgx Scan and a function that copies the scanned value into object.
func (f *Field) ScanTarget(object any) (destination any, finish func()) {
	target := f.Addr(object)
	switch f.Kind {
	case KindForeignKey:
		var id *int64
		return &id, func() {
			if id != nil {
				target.FieldByName("ID").SetInt(*id)
			} else {
				target.FieldByName("ID").SetInt(0)
			}
		}
	case KindUUID:
		var uuid *[16]byte
		return &uuid, func() {
			if uuid == nil {
				target.Set(reflect.Zero(f.GoType))
				return
			}
			value := reflect.New(f.BaseType()).Elem()
			reflect.Copy(value, reflect.ValueOf(uuid).Elem())
			if f.IsPointer() {
				pointer := reflect.New(f.BaseType())
				pointer.Elem().Set(value)
				target.Set(pointer)
			} else {
				target.Set(value)
			}
		}
	}
	return target.Addr().Interface(), nil
}

// DatabaseType is the Postgres column type.
func (f *Field) DatabaseType() string {
	switch f.Kind {
	case KindAutoID, KindInt, KindInt64, KindForeignKey:
		return "bigint"
	case KindInt32:
		return "integer"
	case KindChar:
		return fmt.Sprintf("varchar(%d)", f.MaxLength)
	case KindText:
		return "text"
	case KindFloat:
		return "double precision"
	case KindBool:
		return "boolean"
	case KindDateTime:
		return "timestamptz"
	case KindBytes:
		return "bytea"
	case KindUUID:
		return "uuid"
	case KindJSON:
		return "jsonb"
	}
	return "text"
}

// ChoiceLabel returns a human label for a choice value.
func ChoiceLabel(value any) string {
	return Humanize(fmt.Sprint(value))
}

var timeType = reflect.TypeFor[time.Time]()
