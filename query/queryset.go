package query

import (
	"context"
	"fmt"
	"iter"
	"reflect"

	"github.com/yoav/djan-go/models"
)

// QuerySet is a lazy, immutable query over model T. Every method returns a new QuerySet.
type QuerySet[T any] struct {
	dynamic Dynamic
}

// Objects returns a QuerySet over all rows of model T.
func Objects[T any]() QuerySet[T] {
	info, ok := models.InfoFor[T]()
	if !ok {
		return QuerySet[T]{dynamic: Dynamic{err: fmt.Errorf("query: %s is not registered", reflect.TypeFor[T]())}}
	}
	return QuerySet[T]{dynamic: ForModel(info)}
}

// FromDynamic converts an untyped queryset back to a typed one.
func FromDynamic[T any](dynamic Dynamic) QuerySet[T] {
	if dynamic.info != nil && dynamic.info.Type != reflect.TypeFor[T]() {
		dynamic = dynamic.withError(fmt.Errorf("query: %s queryset used as %s", dynamic.info.Name, reflect.TypeFor[T]()))
	}
	return QuerySet[T]{dynamic: dynamic}
}

// Dynamic exposes the untyped queryset.
func (qs QuerySet[T]) Dynamic() Dynamic { return qs.dynamic }

func (qs QuerySet[T]) probe() *T {
	if qs.dynamic.info == nil {
		return new(T)
	}
	return (*T)(qs.dynamic.info.Probe())
}

// Where filters with a typed condition built from the model's probe:
//
//	qs.Where(func(p *Post) query.Cond { return query.Eq(&p.Published, true) })
func (qs QuerySet[T]) Where(build func(model *T) Cond) QuerySet[T] {
	return QuerySet[T]{dynamic: qs.dynamic.Filter(build(qs.probe()))}
}

// WhereNot excludes rows matching the typed condition.
func (qs QuerySet[T]) WhereNot(build func(model *T) Cond) QuerySet[T] {
	return QuerySet[T]{dynamic: qs.dynamic.Filter(Not(build(qs.probe())))}
}

// Filter adds conditions (typed or string-based Q).
func (qs QuerySet[T]) Filter(conds ...Cond) QuerySet[T] {
	return QuerySet[T]{dynamic: qs.dynamic.Filter(conds...)}
}

// Exclude removes rows matching all conditions.
func (qs QuerySet[T]) Exclude(conds ...Cond) QuerySet[T] {
	return QuerySet[T]{dynamic: qs.dynamic.Exclude(conds...)}
}

// And intersects two querysets.
func (qs QuerySet[T]) And(other QuerySet[T]) QuerySet[T] {
	return QuerySet[T]{dynamic: qs.dynamic.And(other.dynamic)}
}

// OrderBy sets a typed ordering:
//
//	qs.OrderBy(func(p *Post) []query.Order { return query.By(query.Desc(&p.CreatedAt)) })
func (qs QuerySet[T]) OrderBy(build func(model *T) []Order) QuerySet[T] {
	return QuerySet[T]{dynamic: qs.dynamic.orderByTerms(build(qs.probe()))}
}

// OrderByNames is the string fallback for ordering ("-created_at").
func (qs QuerySet[T]) OrderByNames(names ...string) QuerySet[T] {
	return QuerySet[T]{dynamic: qs.dynamic.OrderByNames(names...)}
}

// Limit caps the number of rows.
func (qs QuerySet[T]) Limit(count int) QuerySet[T] {
	return QuerySet[T]{dynamic: qs.dynamic.Limit(count)}
}

// Offset skips rows.
func (qs QuerySet[T]) Offset(count int) QuerySet[T] {
	return QuerySet[T]{dynamic: qs.dynamic.Offset(count)}
}

// ToSQL returns the SELECT statement and arguments, useful for debugging.
func (qs QuerySet[T]) ToSQL() (string, []any, error) { return qs.dynamic.ToSQL() }

func typed[T any](objects []any) []*T {
	result := make([]*T, len(objects))
	for index, object := range objects {
		result[index] = object.(*T)
	}
	return result
}

// All runs the query.
func (qs QuerySet[T]) All(ctx context.Context) ([]*T, error) {
	objects, err := qs.dynamic.All(ctx)
	return typed[T](objects), err
}

// Iter streams results: for post, err := range qs.Iter(ctx) { ... }
func (qs QuerySet[T]) Iter(ctx context.Context) iter.Seq2[*T, error] {
	return func(yield func(*T, error) bool) {
		stopped := false
		err := qs.dynamic.Iterate(ctx, func(object any) error {
			if !yield(object.(*T), nil) {
				stopped = true
				return errStopIteration
			}
			return nil
		})
		if err != nil && err != errStopIteration && !stopped {
			yield(nil, err)
		}
	}
}

var errStopIteration = fmt.Errorf("query: iteration stopped")

// Get returns the single object matching the extra typed condition (pass nil for none).
func (qs QuerySet[T]) Get(ctx context.Context, build func(model *T) Cond) (*T, error) {
	dynamic := qs.dynamic
	if build != nil {
		dynamic = dynamic.Filter(build(qs.probe()))
	}
	object, err := dynamic.Get(ctx)
	if err != nil {
		return nil, err
	}
	return object.(*T), nil
}

// GetByPK returns the object with the primary key.
func (qs QuerySet[T]) GetByPK(ctx context.Context, primaryKey int64) (*T, error) {
	object, err := qs.dynamic.GetByPK(ctx, primaryKey)
	if err != nil {
		return nil, err
	}
	return object.(*T), nil
}

// First returns the first object, or ErrNotFound.
func (qs QuerySet[T]) First(ctx context.Context) (*T, error) {
	object, err := qs.dynamic.First(ctx)
	if err != nil {
		return nil, err
	}
	return object.(*T), nil
}

// Count returns the number of rows.
func (qs QuerySet[T]) Count(ctx context.Context) (int64, error) { return qs.dynamic.Count(ctx) }

// Exists reports whether any row matches.
func (qs QuerySet[T]) Exists(ctx context.Context) (bool, error) { return qs.dynamic.Exists(ctx) }

// PrimaryKeys returns the primary keys of matching rows.
func (qs QuerySet[T]) PrimaryKeys(ctx context.Context) ([]int64, error) {
	return qs.dynamic.PrimaryKeys(ctx)
}

// Values returns maps of the named fields (string fallback).
func (qs QuerySet[T]) Values(ctx context.Context, names ...string) ([]map[string]any, error) {
	return qs.dynamic.Values(ctx, names...)
}

// Delete removes matching rows.
func (qs QuerySet[T]) Delete(ctx context.Context) (int64, error) { return qs.dynamic.Delete(ctx) }

// Update sets columns on matching rows:
//
//	qs.Update(ctx, func(p *Post) []query.Assign { return []query.Assign{query.Set(&p.Published, true)} })
func (qs QuerySet[T]) Update(ctx context.Context, build func(model *T) []Assign) (int64, error) {
	if qs.dynamic.err != nil {
		return 0, qs.dynamic.err
	}
	assignments := map[*models.Field]any{}
	for _, assign := range build(qs.probe()) {
		field, ok := qs.dynamic.info.FieldAt(assign.reference.pointer, assign.reference.typ)
		if !ok {
			return 0, fmt.Errorf("query: Update: pointer is not a declared field of %s", qs.dynamic.info.Name)
		}
		if field.PrimaryKey {
			return 0, fmt.Errorf("query: Update cannot change the primary key")
		}
		assignments[field] = assign.value
	}
	return qs.dynamic.UpdateFields(ctx, assignments)
}

// Relation is a foreign key to preload.
type Relation struct{ reference fieldReference }

// Related names a foreign key for Preload.
func Related[R any](field *models.FK[R]) Relation { return Relation{reference: referenceOf(field)} }

// Preload runs the query and loads the given foreign keys with one extra query each.
func (qs QuerySet[T]) Preload(ctx context.Context, build func(model *T) []Relation) ([]*T, error) {
	objects, err := qs.dynamic.All(ctx)
	if err != nil {
		return nil, err
	}
	for _, relation := range build(qs.probe()) {
		field, ok := qs.dynamic.info.FieldAt(relation.reference.pointer, relation.reference.typ)
		if !ok {
			return nil, fmt.Errorf("query: Preload: pointer is not a foreign key of %s", qs.dynamic.info.Name)
		}
		if err := PreloadRelated(ctx, objects, field); err != nil {
			return nil, err
		}
	}
	return typed[T](objects), nil
}

func reflectArguments(values ...any) []reflect.Value {
	arguments := make([]reflect.Value, len(values))
	for index, value := range values {
		arguments[index] = reflect.ValueOf(value)
	}
	return arguments
}
