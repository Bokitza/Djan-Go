package importexport

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/yoav-rogat/djan-go/db"
	"github.com/yoav-rogat/djan-go/models"
	"github.com/yoav-rogat/djan-go/query"
)

// RowType is the outcome of importing one row.
type RowType string

const (
	RowNew     RowType = "new"
	RowUpdate  RowType = "update"
	RowSkip    RowType = "skip"
	RowError   RowType = "error"   // the row could not be processed or saved
	RowInvalid RowType = "invalid" // the row has validation errors
)

// RowResult describes what happened to one dataset row.
type RowResult struct {
	// Number is the 1-based position of the row in the dataset (excluding headers).
	Number     int
	Type       RowType
	ObjectID   int64
	ObjectRepr string
	// Error is set for RowError rows.
	Error error
	// ValidationErrors maps column headers to messages for RowInvalid rows. The empty key holds
	// errors about the whole row (from the model's Validate method).
	ValidationErrors map[string]string
}

// Result is the outcome of an import.
type Result struct {
	Rows   []RowResult
	Totals map[RowType]int
	// Committed reports whether the changes were kept. Dry runs and imports with errors or
	// invalid rows are rolled back.
	Committed bool
}

// HasErrors reports whether any row failed with an error.
func (r *Result) HasErrors() bool { return r.Totals[RowError] > 0 }

// HasValidationErrors reports whether any row was invalid.
func (r *Result) HasValidationErrors() bool { return r.Totals[RowInvalid] > 0 }

// ImportOptions control an import.
type ImportOptions struct {
	// DryRun processes every row and reports the result, then rolls back.
	DryRun bool
	// RaiseErrors stops at the first failed or invalid row and returns it as an error.
	RaiseErrors bool
}

// ImportError is returned by Import with RaiseErrors when a row fails.
type ImportError struct{ Row RowResult }

func (e *ImportError) Error() string {
	if e.Row.Error != nil {
		return fmt.Sprintf("importexport: row %d: %v", e.Row.Number, e.Row.Error)
	}
	headers := make([]string, 0, len(e.Row.ValidationErrors))
	for header := range e.Row.ValidationErrors {
		headers = append(headers, header)
	}
	sort.Strings(headers)
	messages := make([]string, len(headers))
	for index, header := range headers {
		messages[index] = e.Row.ValidationErrors[header]
		if header != "" {
			messages[index] = header + ": " + messages[index]
		}
	}
	return fmt.Sprintf("importexport: row %d is invalid: %s", e.Row.Number, strings.Join(messages, "; "))
}

func (e *ImportError) Unwrap() error { return e.Row.Error }

var errRollback = errors.New("importexport: rollback")

// Import creates or updates objects from the dataset rows inside a transaction. Row problems are
// reported in the result; the returned error is for configuration, database or RaiseErrors failures.
func (r Resource[T]) Import(ctx context.Context, dataset *Dataset, options ImportOptions) (*Result, error) {
	resolved, err := r.resolve()
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, header := range dataset.Headers {
		present[header] = true
	}
	importIDFields := resolved.importIDFields
	for _, column := range importIDFields {
		if present[column.header] {
			continue
		}
		if len(importIDFields) == 1 && column.field.PrimaryKey {
			importIDFields = nil
			break
		}
		return nil, fmt.Errorf("importexport: the dataset has no %q column, which is needed to find existing objects", column.header)
	}

	result := &Result{Totals: map[RowType]int{}}
	err = db.Atomic(ctx, func(ctx context.Context) error {
		for index := range dataset.Rows {
			rowResult := r.importRow(ctx, resolved, importIDFields, dataset.Row(index))
			rowResult.Number = index + 1
			result.Rows = append(result.Rows, rowResult)
			result.Totals[rowResult.Type]++
			if options.RaiseErrors && (rowResult.Type == RowError || rowResult.Type == RowInvalid) {
				return &ImportError{Row: rowResult}
			}
		}
		if options.DryRun || result.HasErrors() || result.HasValidationErrors() {
			return errRollback
		}
		return nil
	})
	if errors.Is(err, errRollback) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Committed = true
	return result, nil
}

func (r Resource[T]) importRow(ctx context.Context, resolved *resolvedResource, importIDFields []column, row Row) RowResult {
	info := resolved.info
	if r.BeforeImportRow != nil {
		if err := r.BeforeImportRow(ctx, row); err != nil {
			return RowResult{Type: RowError, Error: err}
		}
	}
	candidates := query.ForModel(info)
	if r.QuerySet != nil {
		candidates = r.QuerySet(ctx).Dynamic()
	}
	object, isNew, validationErrors, err := findInstance[T](ctx, info, candidates, importIDFields, row)
	if err != nil {
		return RowResult{Type: RowError, Error: err}
	}
	if len(validationErrors) > 0 {
		return RowResult{Type: RowInvalid, ValidationErrors: validationErrors}
	}
	original := *object

	validationErrors = map[string]string{}
	for _, column := range resolved.columns {
		cell, present := row[column.header]
		if !present || !column.importable() {
			continue
		}
		value, err := column.widget.Clean(ctx, cell)
		if err == nil && column.field.Kind == models.KindForeignKey && !column.field.Nullable && value == int64(0) {
			err = errRequired
		}
		if err == nil {
			err = column.field.Set(object, value)
		}
		if err != nil {
			validationErrors[column.header] = err.Error()
		}
	}
	if validator, ok := any(object).(query.Validator); ok && len(validationErrors) == 0 {
		if err := validator.Validate(); err != nil {
			validationErrors[""] = err.Error()
		}
	}
	rowResult := RowResult{ObjectID: info.PKOf(object)}
	if !isNew {
		rowResult.ObjectRepr = info.DisplayString(object)
	}
	if len(validationErrors) > 0 {
		rowResult.Type, rowResult.ValidationErrors = RowInvalid, validationErrors
		return rowResult
	}

	skip := false
	if r.SkipRow != nil {
		skip = r.SkipRow(ctx, object, &original, row, isNew)
	} else if resolved.skipUnchanged && !isNew {
		skip = unchanged(resolved, object, &original)
	}
	if skip {
		rowResult.Type = RowSkip
		return rowResult
	}

	err = db.Atomic(ctx, func(ctx context.Context) error {
		if r.BeforeSaveInstance != nil {
			if err := r.BeforeSaveInstance(ctx, object, row, isNew); err != nil {
				return err
			}
		}
		if err := query.SaveObject(ctx, info, object); err != nil {
			return err
		}
		if r.AfterSaveInstance != nil {
			return r.AfterSaveInstance(ctx, object, row, isNew)
		}
		return nil
	})
	if err != nil {
		rowResult.Type, rowResult.Error = RowError, err
		return rowResult
	}
	rowResult.Type = RowUpdate
	if isNew {
		rowResult.Type = RowNew
	}
	rowResult.ObjectID, rowResult.ObjectRepr = info.PKOf(object), info.DisplayString(object)
	return rowResult
}

// findInstance loads the object matching the row's import ID cells, or returns a new object
// with defaults applied when the cells are empty or nothing matches.
func findInstance[T any](ctx context.Context, info *models.ModelInfo, candidates query.Dynamic, importIDFields []column, row Row) (*T, bool, map[string]string, error) {
	var conditions []query.Cond
	validationErrors := map[string]string{}
	empty := true
	for _, column := range importIDFields {
		cell := row[column.header]
		empty = empty && strings.TrimSpace(cell) == ""
		value, err := column.widget.Clean(ctx, cell)
		if err != nil {
			validationErrors[column.header] = err.Error()
			continue
		}
		databaseValue := column.field.ToDatabase(value)
		if column.field.Kind == models.KindForeignKey && databaseValue == int64(0) {
			databaseValue = nil
		}
		if databaseValue == nil {
			conditions = append(conditions, query.FieldLookup(column.field, "isnull", true))
		} else {
			conditions = append(conditions, query.FieldLookup(column.field, "exact", databaseValue))
		}
	}
	if !empty && len(validationErrors) > 0 {
		return nil, false, validationErrors, nil
	}
	if !empty {
		existing, err := candidates.Get(ctx, conditions...)
		if err == nil {
			return existing.(*T), false, nil, nil
		}
		if !errors.Is(err, query.ErrNotFound) {
			return nil, false, nil, err
		}
	}
	object := info.New().(*T)
	info.ApplyDefaults(object)
	return object, true, nil, nil
}

func unchanged(resolved *resolvedResource, object, original any) bool {
	for _, column := range resolved.columns {
		if column.importable() && !sameValue(column.field.Get(object), column.field.Get(original)) {
			return false
		}
	}
	return true
}

func sameValue(first, second any) bool {
	if firstTime, ok := first.(time.Time); ok {
		secondTime, ok := second.(time.Time)
		return ok && firstTime.Equal(secondTime)
	}
	return reflect.DeepEqual(first, second)
}
