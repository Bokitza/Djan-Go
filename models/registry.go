package models

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"unsafe"
)

// ModelInfo is the cached metadata of a registered model.
type ModelInfo struct {
	Type              reflect.Type
	Name              string
	App               string
	Table             string
	VerboseName       string
	VerboseNamePlural string
	Fields            []*Field
	PK                *Field
	Ordering          []OrderField
	UniqueTogether    [][]*Field
	Indexes           [][]*Field
	RegisteredAt      string

	probe   unsafe.Pointer
	size    uintptr
	bySlot  map[slot]*Field
	byName  map[string]*Field
	byGoKey map[string]*Field
}

// Label is "<app>.<Model>".
func (m *ModelInfo) Label() string { return m.App + "." + m.Name }

// Key is the lower case model name used in URLs and permission codenames.
func (m *ModelInfo) Key() string { return strings.ToLower(m.Name) }

// Probe returns the shared probe instance used to resolve field pointers. Never write to it.
func (m *ModelInfo) Probe() unsafe.Pointer { return m.probe }

// FieldByName finds a field by lookup name (snake_case, FKs without _id), column or Go name.
func (m *ModelInfo) FieldByName(name string) *Field {
	if field, ok := m.byName[name]; ok {
		return field
	}
	return m.byGoKey[name]
}

func (m *ModelInfo) fieldByGoName(name string) *Field {
	for _, field := range m.Fields {
		if field.Name == name {
			return field
		}
	}
	return nil
}

// FieldAt resolves a field pointer (taken from this model's probe) to its field.
func (m *ModelInfo) FieldAt(pointer unsafe.Pointer, typ reflect.Type) (*Field, bool) {
	if uintptr(pointer) < uintptr(m.probe) || uintptr(pointer) >= uintptr(m.probe)+m.size {
		return nil, false
	}
	field, ok := m.bySlot[slot{uintptr(pointer) - uintptr(m.probe), typ}]
	return field, ok
}

// Contains reports whether pointer points inside this model's probe.
func (m *ModelInfo) Contains(pointer unsafe.Pointer) bool {
	return uintptr(pointer) >= uintptr(m.probe) && uintptr(pointer) < uintptr(m.probe)+m.size
}

// New allocates a zero object of the model and returns a pointer to it.
func (m *ModelInfo) New() any { return reflect.New(m.Type).Interface() }

// ApplyDefaults sets declared defaults on object.
func (m *ModelInfo) ApplyDefaults(object any) {
	for _, field := range m.Fields {
		if field.HasDefault {
			_ = field.Set(object, field.Default)
		}
	}
}

// PKOf returns the primary key of object.
func (m *ModelInfo) PKOf(object any) int64 {
	return m.PK.Addr(object).Int()
}

// DisplayString returns the String() of object or "<Model> #<id>".
func (m *ModelInfo) DisplayString(object any) string {
	if stringer, ok := object.(fmt.Stringer); ok {
		return stringer.String()
	}
	return fmt.Sprintf("%s #%d", m.VerboseName, m.PKOf(object))
}

// SchemaError lists everything wrong with a model declaration.
type SchemaError struct {
	Model    string
	Problems []error
}

func (e *SchemaError) Error() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "models: invalid schema for %s:", e.Model)
	for _, problem := range e.Problems {
		builder.WriteString("\n  ")
		builder.WriteString(problem.Error())
	}
	return builder.String()
}

var registry = struct {
	sync.RWMutex
	byType map[reflect.Type]*ModelInfo
	order  []*ModelInfo
}{byType: map[reflect.Type]*ModelInfo{}}

var modelType = reflect.TypeFor[Model]()

// Register registers model T. T must embed models.Model and *T must have a Schema method.
// It panics with a descriptive SchemaError when the declaration is invalid.
func Register[T any, PT interface {
	*T
	Schema(*Schema)
}]() *ModelInfo {
	info, err := register[T, PT](callerLocation(1))
	if err != nil {
		panic(err)
	}
	return info
}

func register[T any, PT interface {
	*T
	Schema(*Schema)
}](location string) (*ModelInfo, error) {
	typ := reflect.TypeFor[T]()
	registry.RLock()
	existing, exists := registry.byType[typ]
	registry.RUnlock()
	if exists {
		return nil, &SchemaError{Model: typ.String(), Problems: []error{fmt.Errorf("%s: already registered at %s", location, existing.RegisteredAt)}}
	}
	if typ.Kind() != reflect.Struct {
		return nil, &SchemaError{Model: typ.String(), Problems: []error{fmt.Errorf("%s: models must be structs", location)}}
	}

	probe := new(T)
	info := &ModelInfo{
		Type:         typ,
		Name:         typ.Name(),
		App:          packageName(typ.PkgPath()),
		RegisteredAt: location,
		probe:        unsafe.Pointer(probe),
		size:         typ.Size(),
	}
	schema := &Schema{
		info:       info,
		base:       unsafe.Pointer(probe),
		size:       typ.Size(),
		candidates: map[slot]candidate{},
		ignored:    map[string]bool{},
	}
	collectCandidates(typ, 0, nil, "", schema.candidates)

	modelIndex := -1
	for index := 0; index < typ.NumField(); index++ {
		if structField := typ.Field(index); structField.Anonymous && structField.Type == modelType {
			modelIndex = index
		}
	}
	if modelIndex < 0 {
		return nil, &SchemaError{Model: typ.String(), Problems: []error{fmt.Errorf("%s: %s must embed models.Model", location, typ.Name())}}
	}
	modelOffset := typ.Field(modelIndex).Offset
	declareAuto := func(name string, kind Kind, fieldType reflect.Type) *Field {
		pointer := unsafe.Add(unsafe.Pointer(probe), modelOffset+modelType.Field(indexOf(modelType, name)).Offset)
		field := schema.declare(location, pointer, fieldType, kind)
		field.Editable = false
		field.Auto = true
		return field
	}
	info.PK = declareAuto("ID", KindAutoID, reflect.TypeFor[int64]())
	info.PK.PrimaryKey = true
	info.PK.VerboseName = "ID"
	createdAt := declareAuto("CreatedAt", KindDateTime, timeType)
	updatedAt := declareAuto("UpdatedAt", KindDateTime, timeType)

	PT(probe).Schema(schema)

	// keep timestamps last
	info.Fields = slices.DeleteFunc(info.Fields, func(field *Field) bool { return field == createdAt || field == updatedAt })
	info.Fields = append(info.Fields, createdAt, updatedAt)

	finalize(info, schema)
	if len(schema.errors) > 0 {
		return nil, &SchemaError{Model: info.Label(), Problems: schema.errors}
	}

	registry.Lock()
	defer registry.Unlock()
	registry.byType[typ] = info
	registry.order = append(registry.order, info)
	return info, nil
}

func indexOf(typ reflect.Type, name string) int {
	field, _ := typ.FieldByName(name)
	return field.Index[0]
}

func packageName(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		path = path[index+1:]
	}
	return strings.ReplaceAll(path, "-", "_")
}

var foreignKeyInterface = reflect.TypeFor[foreignKey]()

func collectCandidates(typ reflect.Type, baseOffset uintptr, baseIndex []int, prefix string, into map[slot]candidate) {
	for index := 0; index < typ.NumField(); index++ {
		structField := typ.Field(index)
		if !structField.IsExported() {
			continue
		}
		fieldIndex := append(slices.Clone(baseIndex), index)
		name := structField.Name
		if prefix != "" {
			name = prefix + "." + structField.Name
		}
		offset := baseOffset + structField.Offset
		if structField.Anonymous && structField.Type.Kind() == reflect.Struct {
			collectCandidates(structField.Type, offset, fieldIndex, prefix, into)
			continue
		}
		key := slot{offset, structField.Type}
		if _, taken := into[key]; !taken {
			into[key] = candidate{index: fieldIndex, name: name}
		}
		if structField.Type.Kind() == reflect.Struct && structField.Type != timeType && structField.Type != decimalType && !structField.Type.Implements(foreignKeyInterface) {
			collectCandidates(structField.Type, offset, fieldIndex, name, into)
		}
	}
}

func finalize(info *ModelInfo, schema *Schema) {
	if info.Table == "" {
		info.Table = info.App + "_" + SnakeCase(info.Name)
	}
	if info.VerboseName == "" {
		info.VerboseName = Humanize(info.Name)
	}
	if info.VerboseNamePlural == "" {
		info.VerboseNamePlural = Pluralize(info.VerboseName)
	}

	declared := map[string]bool{}
	for _, field := range info.Fields {
		declared[field.Name] = true
	}
	checkUndeclared(info, schema, info.Type, "", declared)

	info.bySlot = map[slot]*Field{}
	info.byName = map[string]*Field{}
	info.byGoKey = map[string]*Field{}
	columns := map[string]*Field{}
	for _, field := range info.Fields {
		if field.Name == "" {
			continue
		}
		if other, taken := columns[field.Column]; taken {
			schema.errorf(field.DeclaredAt, "column %q is used by both %s and %s", field.Column, other.Name, field.Name)
		}
		columns[field.Column] = field
		info.bySlot[slot{field.Offset, field.GoType}] = field
		if field.Kind == KindForeignKey {
			info.bySlot[slot{field.Offset, reflect.TypeFor[int64]()}] = field
		}
		if field.IsPointer() {
			info.bySlot[slot{field.Offset, field.GoType.Elem()}] = field
		}
		info.byName[field.LookupName()] = field
		info.byName[field.Column] = field
		info.byGoKey[field.Name] = field

		if field.Kind == KindChar && field.MaxLength <= 0 {
			schema.errorf(field.DeclaredAt, "Char field %s needs a positive max length", field.Name)
		}
		if field.HasDefault && len(field.Choices) > 0 && !slices.Contains(field.Choices, field.Default) {
			schema.errorf(field.DeclaredAt, "default %v of %s is not one of its choices %v", field.Default, field.Name, field.Choices)
		}
		if field.Kind == KindForeignKey && field.OnDelete == SetNull && !field.Nullable {
			schema.errorf(field.DeclaredAt, "foreign key %s uses OnDelete(SetNull) but is not Nullable()", field.Name)
		}
	}

	for _, term := range schema.ordering {
		field, ok := info.FieldAt(term.Pointer, term.Type)
		if !ok {
			schema.errorf(schema.orderingAt, "Ordering: pointer is not a declared field of %s", info.Name)
			continue
		}
		info.Ordering = append(info.Ordering, OrderField{Field: field, Descending: term.Descending})
	}
	resolveGroup := func(group []any, location string) []*Field {
		var fields []*Field
		for _, pointer := range group {
			value := reflect.ValueOf(pointer)
			if value.Kind() != reflect.Pointer {
				schema.errorf(location, "expected a field pointer, got %T", pointer)
				continue
			}
			field, ok := info.FieldAt(value.UnsafePointer(), value.Type().Elem())
			if !ok {
				schema.errorf(location, "pointer is not a declared field of %s", info.Name)
				continue
			}
			fields = append(fields, field)
		}
		return fields
	}
	for index, group := range schema.uniqueTogether {
		info.UniqueTogether = append(info.UniqueTogether, resolveGroup(group, schema.compositeAt[index]))
	}
	for _, group := range schema.indexes {
		info.Indexes = append(info.Indexes, resolveGroup(group, info.RegisteredAt))
	}
}

func checkUndeclared(info *ModelInfo, schema *Schema, typ reflect.Type, prefix string, declared map[string]bool) {
	for index := 0; index < typ.NumField(); index++ {
		structField := typ.Field(index)
		if !structField.IsExported() || structField.Type == modelType {
			continue
		}
		name := structField.Name
		if prefix != "" {
			name = prefix + "." + name
		}
		if structField.Anonymous && structField.Type.Kind() == reflect.Struct {
			checkUndeclared(info, schema, structField.Type, prefix, declared)
			continue
		}
		if declared[name] || schema.ignored[name] {
			continue
		}
		hasDeclaredChild := false
		for declaredName := range declared {
			if strings.HasPrefix(declaredName, name+".") {
				hasDeclaredChild = true
			}
		}
		if !hasDeclaredChild {
			schema.errorf(info.RegisteredAt, "field %s.%s is neither declared in Schema nor ignored with s.Ignore(&p.%s)", info.Name, name, name)
		}
	}
}

func infoForType(typ reflect.Type) (*ModelInfo, bool) {
	registry.RLock()
	defer registry.RUnlock()
	info, ok := registry.byType[typ]
	return info, ok
}

// InfoFor returns the metadata of model T.
func InfoFor[T any]() (*ModelInfo, bool) { return infoForType(reflect.TypeFor[T]()) }

// InfoForType returns the metadata of the model with the given struct type.
func InfoForType(typ reflect.Type) (*ModelInfo, bool) { return infoForType(typ) }

// MustInfo returns the metadata of model T or panics with a helpful message.
func MustInfo[T any]() *ModelInfo {
	info, ok := InfoFor[T]()
	if !ok {
		panic(fmt.Sprintf("models: %s is not registered; call models.Register[%s]() in an init function", reflect.TypeFor[T](), reflect.TypeFor[T]().Name()))
	}
	return info
}

// ProbeOf returns the probe of model T for resolving field pointers.
func ProbeOf[T any]() (*T, error) {
	info, ok := InfoFor[T]()
	if !ok {
		return nil, fmt.Errorf("models: %s is not registered", reflect.TypeFor[T]())
	}
	return (*T)(info.probe), nil
}

// All returns every registered model sorted by app and name.
func All() []*ModelInfo {
	registry.RLock()
	all := slices.Clone(registry.order)
	registry.RUnlock()
	sort.SliceStable(all, func(a, b int) bool {
		if all[a].App != all[b].App {
			return all[a].App < all[b].App
		}
		return all[a].Name < all[b].Name
	})
	return all
}

// ByLabel finds a model by app label and model key ("blog", "post").
func ByLabel(app, key string) (*ModelInfo, bool) {
	for _, info := range All() {
		if info.App == app && info.Key() == key {
			return info, true
		}
	}
	return nil, false
}

// ResolvePointer finds the registered field a pointer refers to. Pointers must be taken from a
// probe handed out by the framework (a Schema receiver or a query/admin closure argument).
func ResolvePointer(pointer unsafe.Pointer, typ reflect.Type) (*Field, error) {
	registry.RLock()
	defer registry.RUnlock()
	for _, info := range registry.order {
		if !info.Contains(pointer) {
			continue
		}
		if field, ok := info.FieldAt(pointer, typ); ok {
			return field, nil
		}
		return nil, fmt.Errorf("models: pointer of type *%s is not a declared field of %s", typ, info.Name)
	}
	return nil, fmt.Errorf("models: pointer of type *%s does not belong to a model probe; take field pointers from the closure argument (e.g. &p.Title)", typ)
}

// Validate checks cross-model consistency: foreign key targets and table names.
func Validate() error {
	var problems []error
	tables := map[string]*ModelInfo{}
	for _, info := range All() {
		if other, taken := tables[info.Table]; taken {
			problems = append(problems, fmt.Errorf("%s: table %q is used by both %s and %s", info.RegisteredAt, info.Table, other.Label(), info.Label()))
		}
		tables[info.Table] = info
		for _, field := range info.Fields {
			if field.Kind == KindForeignKey && field.Related() == nil {
				problems = append(problems, fmt.Errorf("%s: foreign key %s points to %s, which is not registered", field.DeclaredAt, field, field.RelatedType))
			}
		}
	}
	return errors.Join(problems...)
}

// MustValidate fails the test when Validate reports problems.
func MustValidate(t interface {
	Helper()
	Fatal(arguments ...any)
}) {
	t.Helper()
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
}
