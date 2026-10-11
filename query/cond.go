// Package query implements lazy, immutable, generic querysets over registered models.
package query

import (
	"cmp"
	"reflect"
	"time"
	"unsafe"

	"github.com/shopspring/decimal"
	"github.com/yoav-rogat/djan-go/models"
)

// Cond is a filter condition. Build conditions with the typed lookups (Eq, Gt, IContains, ...),
// combine them with And, Or and Not, follow foreign keys with Rel, or use the string-based Q.
type Cond interface{ isCond() }

type fieldReference struct {
	pointer unsafe.Pointer
	typ     reflect.Type
}

func referenceOf[V any](field *V) fieldReference {
	return fieldReference{pointer: unsafe.Pointer(field), typ: reflect.TypeFor[V]()}
}

type lookupCond struct {
	reference fieldReference
	field     *models.Field // set instead of reference for resolved lookups
	operator  string
	values    []any
}

type relationCond struct {
	reference fieldReference
	field     *models.Field
	inner     Cond
	err       error
}

type logicalCond struct {
	operator string // AND or OR
	conds    []Cond
}

type notCond struct{ inner Cond }

type rawCond struct {
	sql       string
	arguments []any
}

type errorCond struct{ err error }

func (*lookupCond) isCond()   {}
func (*relationCond) isCond() {}
func (*logicalCond) isCond()  {}
func (*notCond) isCond()      {}
func (*rawCond) isCond()      {}
func (*errorCond) isCond()    {}

func lookup[V any](field *V, operator string, values ...any) Cond {
	return &lookupCond{reference: referenceOf(field), operator: operator, values: values}
}

// Ordered are the types usable with Gt, Gte, Lt, Lte and Between.
type Ordered interface {
	cmp.Ordered | time.Time | decimal.Decimal
}

// Eq matches rows where the field equals value. A nil pointer value matches NULL.
func Eq[V comparable](field *V, value V) Cond { return lookup(field, "exact", value) }

// Ne matches rows where the field is distinct from value (NULL-safe).
func Ne[V comparable](field *V, value V) Cond { return lookup(field, "ne", value) }

// In matches rows where the field is one of values. An empty list matches nothing.
func In[V comparable](field *V, values ...V) Cond {
	items := make([]any, len(values))
	for index, value := range values {
		items[index] = value
	}
	return lookup(field, "in", items...)
}

// Gt matches field > value.
func Gt[V Ordered](field *V, value V) Cond { return lookup(field, "gt", value) }

// Gte matches field >= value.
func Gte[V Ordered](field *V, value V) Cond { return lookup(field, "gte", value) }

// Lt matches field < value.
func Lt[V Ordered](field *V, value V) Cond { return lookup(field, "lt", value) }

// Lte matches field <= value.
func Lte[V Ordered](field *V, value V) Cond { return lookup(field, "lte", value) }

// Between matches low <= field <= high.
func Between[V Ordered](field *V, low, high V) Cond { return lookup(field, "range", low, high) }

// Contains is a case-sensitive substring match.
func Contains[V ~string](field *V, value V) Cond { return lookup(field, "contains", value) }

// IContains is a case-insensitive substring match.
func IContains[V ~string](field *V, value V) Cond { return lookup(field, "icontains", value) }

// StartsWith is a case-sensitive prefix match.
func StartsWith[V ~string](field *V, value V) Cond { return lookup(field, "startswith", value) }

// IStartsWith is a case-insensitive prefix match.
func IStartsWith[V ~string](field *V, value V) Cond { return lookup(field, "istartswith", value) }

// EndsWith is a case-sensitive suffix match.
func EndsWith[V ~string](field *V, value V) Cond { return lookup(field, "endswith", value) }

// IExact is a case-insensitive equality match.
func IExact[V ~string](field *V, value V) Cond { return lookup(field, "iexact", value) }

// IsNull matches NULL (or non-NULL when isNull is false) on a nullable field.
func IsNull[V any](field **V, isNull bool) Cond { return lookup(field, "isnull", isNull) }

// IsNullFK matches a nullable foreign key that is NULL (or not).
func IsNullFK[T any](field *models.FK[T], isNull bool) Cond { return lookup(field, "isnull", isNull) }

// Nullable turns a nullable field pointer (**V) into a *V reference so it can be used with value
// lookups such as Gt. NULL values never match those lookups. The result is only a field reference.
func Nullable[V any](field **V) *V { return (*V)(unsafe.Pointer(field)) }

// Rel follows a foreign key: the closure receives the related model's probe.
func Rel[R any](field *models.FK[R], build func(related *R) Cond) Cond {
	probe, err := models.ProbeOf[R]()
	if err != nil {
		return &relationCond{err: err}
	}
	return &relationCond{reference: referenceOf(field), inner: build(probe)}
}

// And matches when every condition matches.
func And(conds ...Cond) Cond { return &logicalCond{operator: "AND", conds: conds} }

// Or matches when any condition matches.
func Or(conds ...Cond) Cond { return &logicalCond{operator: "OR", conds: conds} }

// Not negates a condition.
func Not(cond Cond) Cond { return &notCond{inner: cond} }

// Raw is an escape hatch: a SQL fragment with $1-style placeholders numbered from 1.
// The root table is aliased "t0".
func Raw(sql string, arguments ...any) Cond { return &rawCond{sql: sql, arguments: arguments} }

// FieldLookup builds a condition on an already resolved field. Used by the admin.
func FieldLookup(field *models.Field, operator string, values ...any) Cond {
	return &lookupCond{field: field, operator: operator, values: values}
}

// FieldRel builds a relation condition on an already resolved foreign key field.
func FieldRel(field *models.Field, inner Cond) Cond {
	return &relationCond{field: field, inner: inner}
}

// Assign is a typed column assignment used by Update.
type Assign struct {
	reference fieldReference
	value     any
}

// Set assigns value to field in an Update.
func Set[V any](field *V, value V) Assign { return Assign{reference: referenceOf(field), value: value} }

// Order is an ordering term.
type Order = models.OrderTerm

// Asc orders ascending by a field.
func Asc[V any](field *V) Order { return models.Asc(field) }

// Desc orders descending by a field.
func Desc[V any](field *V) Order { return models.Desc(field) }

// By collects ordering terms.
func By(terms ...Order) []Order { return terms }
