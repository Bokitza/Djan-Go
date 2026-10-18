// Package models defines the model registry and the typed schema builder.
//
// Models are plain structs that embed Model and declare their columns in a
// Schema method using pointers to their own fields:
//
//	type Post struct {
//		models.Model
//		Title string
//	}
//
//	func (p *Post) Schema(s *models.Schema) {
//		s.Char(&p.Title, 200).Index()
//	}
//
//	func init() { models.Register[Post]() }
package models

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

// Model is embedded by every model. Its columns are declared automatically.
type Model struct {
	ID        int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PK returns the primary key.
func (m Model) PK() int64 { return m.ID }

// IsNew reports whether the object has not been inserted yet.
func (m Model) IsNew() bool { return m.ID == 0 }

type primaryKeyed interface{ PK() int64 }

// FK is a foreign key to model T. It stores the related ID and optionally a loaded object.
type FK[T any] struct {
	ID     int64
	object *T
}

// Ref returns a foreign key pointing at the given ID.
func Ref[T any](id int64) FK[T] { return FK[T]{ID: id} }

// RefTo returns a foreign key pointing at an existing object.
func RefTo[T any](object *T) FK[T] {
	foreignKey := FK[T]{}
	foreignKey.Set(object)
	return foreignKey
}

// Set points the foreign key at object and caches it.
func (f *FK[T]) Set(object *T) {
	f.object = object
	f.ID = 0
	if keyed, ok := any(object).(primaryKeyed); ok && object != nil {
		f.ID = keyed.PK()
	}
}

// IsZero reports whether the foreign key points nowhere (a NULL column).
func (f FK[T]) IsZero() bool { return f.ID == 0 }

// Cached returns the loaded object, or nil when it has not been loaded.
func (f FK[T]) Cached() *T {
	if f.object == nil {
		return nil
	}
	if keyed, ok := any(f.object).(primaryKeyed); ok && keyed.PK() != f.ID {
		return nil
	}
	return f.object
}

// Get loads (and caches) the related object.
func (f *FK[T]) Get(ctx context.Context) (*T, error) {
	if cached := f.Cached(); cached != nil {
		return cached, nil
	}
	if f.ID == 0 {
		return nil, nil
	}
	if objectLoader == nil {
		return nil, fmt.Errorf("models: no object loader installed; import the query package")
	}
	loaded, err := objectLoader(ctx, reflect.TypeFor[T](), f.ID)
	if err != nil {
		return nil, err
	}
	f.object = loaded.(*T)
	return f.object, nil
}

func (f FK[T]) foreignKeyTarget() reflect.Type { return reflect.TypeFor[T]() }

// ForeignKeyID returns the stored ID; it lets generic code read any FK without knowing T.
func (f FK[T]) ForeignKeyID() int64 { return f.ID }

type foreignKey interface {
	foreignKeyTarget() reflect.Type
	ForeignKeyID() int64
}

var objectLoader func(ctx context.Context, modelType reflect.Type, id int64) (any, error)

// SetObjectLoader is used by the query package to let FK.Get load objects.
func SetObjectLoader(loader func(ctx context.Context, modelType reflect.Type, id int64) (any, error)) {
	objectLoader = loader
}

// New returns a new object of model T with declared defaults applied.
func New[T any]() *T {
	object := new(T)
	if info, ok := InfoFor[T](); ok {
		info.ApplyDefaults(object)
	}
	return object
}
