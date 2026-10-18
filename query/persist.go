package query

import (
	"context"
	databasesql "database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Bokitza/djan-go/db"
	"github.com/Bokitza/djan-go/models"
)

// BeforeSaver is called before an object is inserted or updated.
type BeforeSaver interface {
	BeforeSave(ctx context.Context) error
}

// AfterSaver is called after an object was inserted or updated.
type AfterSaver interface {
	AfterSave(ctx context.Context) error
}

// Validator is called by the admin before saving.
type Validator interface{ Validate() error }

func init() {
	models.SetObjectLoader(func(ctx context.Context, modelType reflect.Type, id int64) (any, error) {
		info, ok := models.InfoForType(modelType)
		if !ok {
			return nil, fmt.Errorf("query: %s is not registered", modelType)
		}
		return ForModel(info).GetByPK(ctx, id)
	})
}

// Save inserts a new object (ID 0) or updates an existing one.
func Save[T any](ctx context.Context, object *T) error {
	info, ok := models.InfoFor[T]()
	if !ok {
		return fmt.Errorf("query: %s is not registered", reflect.TypeFor[T]())
	}
	return SaveObject(ctx, info, object)
}

// Create inserts a new object; it fails if the object already has an ID.
func Create[T any](ctx context.Context, object *T) error {
	info, ok := models.InfoFor[T]()
	if !ok {
		return fmt.Errorf("query: %s is not registered", reflect.TypeFor[T]())
	}
	if info.PKOf(object) != 0 {
		return fmt.Errorf("query: Create: %s already has id %d", info.Name, info.PKOf(object))
	}
	return SaveObject(ctx, info, object)
}

// DeleteObject deletes one object by primary key.
func DeleteObject[T any](ctx context.Context, object *T) error {
	info, ok := models.InfoFor[T]()
	if !ok {
		return fmt.Errorf("query: %s is not registered", reflect.TypeFor[T]())
	}
	return DeleteAny(ctx, info, object)
}

// SaveObject saves a pointer to a model struct described by info.
func SaveObject(ctx context.Context, info *models.ModelInfo, object any) error {
	if hook, ok := object.(BeforeSaver); ok {
		if err := hook.BeforeSave(ctx); err != nil {
			return err
		}
	}
	executor, err := db.FromContext(ctx)
	if err != nil {
		return err
	}
	dialect := db.DialectFromContext(ctx)
	var columns, placeholders, setters []string
	var arguments []any
	for _, field := range info.Fields {
		if field.Auto {
			continue
		}
		arguments = append(arguments, field.Get(object))
		placeholder := dialect.Placeholder(len(arguments))
		columns = append(columns, QuoteIdentifier(field.Column))
		placeholders = append(placeholders, placeholder)
		setters = append(setters, QuoteIdentifier(field.Column)+" = "+placeholder)
	}
	createdAt, updatedAt := info.FieldByName("created_at"), info.FieldByName("updated_at")
	table := QuoteIdentifier(info.Table)

	if info.PKOf(object) == 0 {
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s, %s, %s", table,
			strings.Join(columns, ", "), strings.Join(placeholders, ", "),
			QuoteIdentifier(info.PK.Column), QuoteIdentifier(createdAt.Column), QuoteIdentifier(updatedAt.Column))
		if len(columns) == 0 {
			sql = fmt.Sprintf("INSERT INTO %s DEFAULT VALUES RETURNING %s, %s, %s", table,
				QuoteIdentifier(info.PK.Column), QuoteIdentifier(createdAt.Column), QuoteIdentifier(updatedAt.Column))
		}
		err = executor.QueryRowContext(ctx, sql, arguments...).Scan(
			info.PK.ScanTarget(object), createdAt.ScanTarget(object), updatedAt.ScanTarget(object))
		if err != nil {
			return fmt.Errorf("query: insert %s: %w", info.Name, err)
		}
	} else {
		arguments = append(arguments, info.PKOf(object))
		setters = append(setters, QuoteIdentifier(updatedAt.Column)+" = "+dialect.Now())
		sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s = %s RETURNING %s", table,
			strings.Join(setters, ", "), QuoteIdentifier(info.PK.Column), dialect.Placeholder(len(arguments)), QuoteIdentifier(updatedAt.Column))
		err = executor.QueryRowContext(ctx, sql, arguments...).Scan(updatedAt.ScanTarget(object))
		if errors.Is(err, databasesql.ErrNoRows) {
			return fmt.Errorf("query: update %s #%d: %w", info.Name, info.PKOf(object), ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("query: update %s: %w", info.Name, err)
		}
	}
	if hook, ok := object.(AfterSaver); ok {
		return hook.AfterSave(ctx)
	}
	return nil
}

// DeleteAny deletes one object described by info.
func DeleteAny(ctx context.Context, info *models.ModelInfo, object any) error {
	count, err := ForModel(info).Filter(FieldLookup(info.PK, "exact", info.PKOf(object))).Delete(ctx)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
