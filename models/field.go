package models

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/shopspring/decimal"
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
	KindDecimal
	KindDate
)

var kindNames = map[Kind]string{
	KindAutoID: "auto id", KindChar: "char", KindText: "text", KindInt: "int", KindInt32: "int32",
	KindInt64: "int64", KindFloat: "float", KindBool: "bool", KindDateTime: "datetime",
	KindBytes: "bytes", KindUUID: "uuid", KindJSON: "json", KindForeignKey: "foreign key",
	KindDecimal: "decimal", KindDate: "date",
}

func (k Kind) String() string { return kindNames[k] }

// IsText reports whether text lookups (contains, startswith, ...) apply to the kind.
func (k Kind) IsText() bool { return k == KindChar || k == KindText }

// IsNumeric reports whether the kind is a number.
func (k Kind) IsNumeric() bool {
	return k == KindInt || k == KindInt32 || k == KindInt64 || k == KindFloat || k == KindAutoID || k == KindDecimal
}

// IsTemporal reports whether the kind is a date or a timestamp.
func (k Kind) IsTemporal() bool { return k == KindDateTime || k == KindDate }

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
	Model      *ModelInfo
	Name       string // Go field path, e.g. "Title" or "Author"
	Column     string
	Kind       Kind
	GoType     reflect.Type // type of the struct field
	Index      []int
	Offset     uintptr
	Nullable   bool
	PrimaryKey bool
	Auto       bool // managed by the database: id, created_at, updated_at
	MaxLength  int
	MaxDigits  int // decimal precision
	Places     int // decimal scale
	Unique     bool
	Indexed    bool
	Blank      bool
	Editable   bool
	HasDefault bool
	Default    any // a value of the field's base (non-pointer) type
	Choices    []any
	// ChoiceLabels maps fmt.Sprint(choice) to its display label (see Column.LabeledChoices).
	ChoiceLabels map[string]string
	VerboseName  string
	HelpText     string
	RelatedType  reflect.Type // FK target
	OnDelete     OnDelete
	// InLabel marks a foreign key that the model's String method reads, so it is loaded before labels render.
	InLabel    bool
	DeclaredAt string
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

// ToDatabase converts a Go value of this field (or of its base type) to a value every database
// driver can encode: timestamps in UTC, dates at UTC midnight, UUIDs and JSON as text.
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
	normalized := normalizeValue(reflected)
	switch f.Kind {
	case KindDateTime:
		if moment, ok := normalized.(time.Time); ok {
			return moment.UTC()
		}
	case KindDate:
		if day, ok := normalized.(time.Time); ok {
			return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		}
	case KindUUID:
		if uuid, ok := normalized.([16]byte); ok {
			return formatUUID(uuid)
		}
	case KindJSON:
		encoded, err := json.Marshal(normalized)
		if err != nil {
			return normalized
		}
		return string(encoded)
	}
	return normalized
}

func formatUUID(uuid [16]byte) string {
	text := hex.EncodeToString(uuid[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}

func parseUUID(text string) ([16]byte, error) {
	var uuid [16]byte
	decoded, err := hex.DecodeString(strings.NewReplacer("-", "", "{", "", "}", "").Replace(text))
	if err != nil || len(decoded) != 16 {
		return uuid, fmt.Errorf("invalid uuid %q", text)
	}
	copy(uuid[:], decoded)
	return uuid, nil
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

// ScanTarget returns a database/sql Scan destination that stores the column value into the field of object.
func (f *Field) ScanTarget(object any) sql.Scanner {
	return fieldScanner{field: f, target: f.Addr(object)}
}

// Value returns the Go value of the field of object: nil for NULL, the ID for foreign keys.
func (f *Field) Value(object any) any {
	value := f.Addr(object)
	if f.Kind == KindForeignKey {
		id := value.FieldByName("ID").Int()
		if id == 0 && f.Nullable {
			return nil
		}
		return id
	}
	if f.IsPointer() {
		if value.IsNil() {
			return nil
		}
		return value.Elem().Interface()
	}
	return value.Interface()
}

type fieldScanner struct {
	field  *Field
	target reflect.Value
}

func (s fieldScanner) Scan(source any) error {
	if s.field.Kind == KindForeignKey {
		id, err := scanInt(source)
		if err != nil {
			return fmt.Errorf("%s: %w", s.field, err)
		}
		s.target.FieldByName("ID").SetInt(id)
		return nil
	}
	if source == nil {
		s.target.Set(reflect.Zero(s.field.GoType))
		return nil
	}
	value := reflect.New(s.field.BaseType()).Elem()
	if err := s.field.decode(source, value); err != nil {
		return fmt.Errorf("%s: %w", s.field, err)
	}
	if s.field.IsPointer() {
		pointer := reflect.New(s.field.BaseType())
		pointer.Elem().Set(value)
		s.target.Set(pointer)
		return nil
	}
	s.target.Set(value)
	return nil
}

var timeLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999",
	time.DateOnly,
}

// decode converts a value returned by a database driver into value, which has the field's base type.
func (f *Field) decode(source any, value reflect.Value) error {
	switch f.Kind {
	case KindJSON:
		var encoded []byte
		switch typed := source.(type) {
		case []byte:
			encoded = typed
		case string:
			encoded = []byte(typed)
		default:
			return fmt.Errorf("cannot decode JSON from %T", source)
		}
		return json.Unmarshal(encoded, value.Addr().Interface())
	case KindUUID:
		var uuid [16]byte
		switch typed := source.(type) {
		case []byte:
			if len(typed) == 16 {
				copy(uuid[:], typed)
				break
			}
			parsed, err := parseUUID(string(typed))
			if err != nil {
				return err
			}
			uuid = parsed
		case string:
			parsed, err := parseUUID(typed)
			if err != nil {
				return err
			}
			uuid = parsed
		case [16]byte:
			uuid = typed
		default:
			return fmt.Errorf("cannot decode a uuid from %T", source)
		}
		reflect.Copy(value, reflect.ValueOf(uuid))
		return nil
	case KindDecimal:
		var amount decimal.Decimal
		if err := amount.Scan(source); err != nil {
			return err
		}
		switch source.(type) {
		case float64, int64:
			amount = decimal.RequireFromString(amount.StringFixed(int32(f.Places)))
		}
		value.Set(reflect.ValueOf(amount))
		return nil
	case KindDateTime, KindDate:
		var text string
		switch typed := source.(type) {
		case time.Time:
			value.Set(reflect.ValueOf(typed))
			return nil
		case string:
			text = typed
		case []byte:
			text = string(typed)
		default:
			return fmt.Errorf("cannot decode a time from %T", source)
		}
		for _, layout := range timeLayouts {
			if parsed, err := time.Parse(layout, text); err == nil {
				value.Set(reflect.ValueOf(parsed))
				return nil
			}
		}
		return fmt.Errorf("cannot parse time %q", text)
	}
	switch value.Kind() {
	case reflect.String:
		switch typed := source.(type) {
		case string:
			value.SetString(typed)
		case []byte:
			value.SetString(string(typed))
		default:
			value.SetString(fmt.Sprint(typed))
		}
		return nil
	case reflect.Bool:
		switch typed := source.(type) {
		case bool:
			value.SetBool(typed)
		case int64:
			value.SetBool(typed != 0)
		case string:
			parsed, err := strconv.ParseBool(typed)
			if err != nil {
				return err
			}
			value.SetBool(parsed)
		default:
			return fmt.Errorf("cannot decode a bool from %T", source)
		}
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number, err := scanInt(source)
		if err != nil {
			return err
		}
		value.SetInt(number)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		number, err := scanInt(source)
		if err != nil {
			return err
		}
		value.SetUint(uint64(number))
		return nil
	case reflect.Float32, reflect.Float64:
		switch typed := source.(type) {
		case float64:
			value.SetFloat(typed)
		case int64:
			value.SetFloat(float64(typed))
		case string, []byte:
			parsed, err := strconv.ParseFloat(fmt.Sprintf("%s", typed), 64)
			if err != nil {
				return err
			}
			value.SetFloat(parsed)
		default:
			return fmt.Errorf("cannot decode a float from %T", source)
		}
		return nil
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			switch typed := source.(type) {
			case []byte:
				value.SetBytes(bytes.Clone(typed))
			case string:
				value.SetBytes([]byte(typed))
			default:
				return fmt.Errorf("cannot decode bytes from %T", source)
			}
			return nil
		}
	}
	reflected := reflect.ValueOf(source)
	if reflected.Type().ConvertibleTo(value.Type()) {
		value.Set(reflected.Convert(value.Type()))
		return nil
	}
	return fmt.Errorf("cannot decode %T into %s", source, value.Type())
}

func scanInt(source any) (int64, error) {
	switch typed := source.(type) {
	case nil:
		return 0, nil
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	case string:
		return strconv.ParseInt(typed, 10, 64)
	}
	return 0, fmt.Errorf("cannot decode an integer from %T", source)
}

// DatabaseType is the canonical (Postgres) column type; db.Dialect.ColumnType translates it for other backends.
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
	case KindDate:
		return "date"
	case KindDecimal:
		return fmt.Sprintf("numeric(%d,%d)", f.MaxDigits, f.Places)
	case KindBytes:
		return "bytea"
	case KindUUID:
		return "uuid"
	case KindJSON:
		return "jsonb"
	}
	return "text"
}

// ChoiceLabel returns the display label of a choice value of the field.
func (f *Field) ChoiceLabel(value any) string {
	if label, ok := f.ChoiceLabels[fmt.Sprint(value)]; ok {
		return label
	}
	return Humanize(fmt.Sprint(value))
}

var (
	timeType    = reflect.TypeFor[time.Time]()
	decimalType = reflect.TypeFor[decimal.Decimal]()
)
