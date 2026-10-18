package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Bokitza/djan-go/db"
	"github.com/Bokitza/djan-go/models"
)

var (
	// ErrNotFound is returned by Get when no row matches.
	ErrNotFound = errors.New("query: object not found")
	// ErrMultipleObjects is returned by Get when more than one row matches.
	ErrMultipleObjects = errors.New("query: multiple objects returned")
)

// Dynamic is the untyped queryset used by framework code (such as the admin) that works with
// *models.ModelInfo instead of a Go type. Application code should use QuerySet[T].
type Dynamic struct {
	info     *models.ModelInfo
	where    []Cond
	order    []models.OrderField
	orderSet bool
	limit    int
	offset   int
	err      error
}

// ForModel returns a Dynamic queryset over all rows of the model.
func ForModel(info *models.ModelInfo) Dynamic { return Dynamic{info: info} }

// Model returns the model metadata.
func (q Dynamic) Model() *models.ModelInfo { return q.info }

// Err returns a construction error, if any.
func (q Dynamic) Err() error { return q.err }

func (q Dynamic) withError(err error) Dynamic {
	if q.err == nil {
		q.err = err
	}
	return q
}

// Filter narrows the queryset to rows matching all conditions.
func (q Dynamic) Filter(conds ...Cond) Dynamic {
	q.where = append(slices.Clip(q.where), conds...)
	return q
}

// Exclude removes rows matching all of the conditions.
func (q Dynamic) Exclude(conds ...Cond) Dynamic {
	return q.Filter(Not(And(conds...)))
}

// And intersects two querysets over the same model (their filters are combined).
func (q Dynamic) And(other Dynamic) Dynamic {
	if other.info != q.info {
		return q.withError(fmt.Errorf("query: cannot combine %s and %s querysets", q.info.Name, other.info.Name))
	}
	q = q.withError(other.err)
	return q.Filter(other.where...)
}

// OrderByNames orders by field names; prefix with "-" for descending. Unknown names are errors.
func (q Dynamic) OrderByNames(names ...string) Dynamic {
	order := make([]models.OrderField, 0, len(names))
	for _, name := range names {
		descending := strings.HasPrefix(name, "-")
		field := q.info.FieldByName(strings.TrimPrefix(name, "-"))
		if field == nil {
			return q.withError(fmt.Errorf("query: cannot order %s by unknown field %q", q.info.Name, name))
		}
		order = append(order, models.OrderField{Field: field, Descending: descending})
	}
	q.order = order
	q.orderSet = true
	return q
}

func (q Dynamic) orderByTerms(terms []Order) Dynamic {
	order := make([]models.OrderField, 0, len(terms))
	for _, term := range terms {
		field, ok := q.info.FieldAt(term.Pointer, term.Type)
		if !ok {
			return q.withError(fmt.Errorf("query: OrderBy: pointer is not a declared field of %s", q.info.Name))
		}
		order = append(order, models.OrderField{Field: field, Descending: term.Descending})
	}
	q.order = order
	q.orderSet = true
	return q
}

// Limit caps the number of rows.
func (q Dynamic) Limit(count int) Dynamic { q.limit = count; return q }

// Offset skips rows.
func (q Dynamic) Offset(count int) Dynamic { q.offset = count; return q }

func (q Dynamic) effectiveOrder() []models.OrderField {
	if q.orderSet {
		return q.order
	}
	return q.info.Ordering
}

func (q Dynamic) compileWhere(c *compiler) (string, error) {
	if q.err != nil {
		return "", q.err
	}
	if q.info == nil {
		return "", errors.New("query: queryset has no model")
	}
	return c.compile(q.info, "t0", And(q.where...))
}

func (q Dynamic) selectSQL(c *compiler, columns string, withOrder bool) (string, error) {
	where, err := q.compileWhere(c)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "SELECT %s FROM %s AS \"t0\" WHERE %s", columns, QuoteIdentifier(q.info.Table), where)
	if withOrder {
		order := q.effectiveOrder()
		if len(order) > 0 {
			terms := make([]string, 0, len(order)+1)
			hasPrimaryKey := false
			for _, term := range order {
				direction := "ASC"
				if term.Descending {
					direction = "DESC"
				}
				terms = append(terms, column("t0", term.Field)+" "+direction)
				hasPrimaryKey = hasPrimaryKey || term.Field.PrimaryKey
			}
			if !hasPrimaryKey {
				terms = append(terms, column("t0", q.info.PK)+" ASC")
			}
			builder.WriteString(" ORDER BY " + strings.Join(terms, ", "))
		}
	}
	if q.limit > 0 {
		builder.WriteString(" LIMIT " + strconv.Itoa(q.limit))
	}
	if q.offset > 0 {
		builder.WriteString(" OFFSET " + strconv.Itoa(q.offset))
	}
	return builder.String(), nil
}

func selectColumns(info *models.ModelInfo) string {
	columns := make([]string, len(info.Fields))
	for index, field := range info.Fields {
		columns[index] = column("t0", field)
	}
	return strings.Join(columns, ", ")
}

// ToSQL returns the SELECT statement and its arguments, in the dialect of the default database
// (Postgres when none is connected).
func (q Dynamic) ToSQL() (string, []any, error) { return q.toSQL(context.Background()) }

func (q Dynamic) toSQL(ctx context.Context) (string, []any, error) {
	c := newCompiler(ctx)
	sql, err := q.selectSQL(c, selectColumns(q.info), true)
	return sql, c.arguments, err
}

func scanFields(rows *sql.Rows, object any, fields []*models.Field) error {
	destinations := make([]any, len(fields))
	for index, field := range fields {
		destinations[index] = field.ScanTarget(object)
	}
	return rows.Scan(destinations...)
}

func scanObject(info *models.ModelInfo, rows *sql.Rows) (any, error) {
	object := info.New()
	if err := scanFields(rows, object, info.Fields); err != nil {
		return nil, fmt.Errorf("query: scanning %s: %w", info.Name, err)
	}
	return object, nil
}

// Iterate streams objects (pointers to the model struct) to fn.
func (q Dynamic) Iterate(ctx context.Context, fn func(object any) error) error {
	sql, arguments, err := q.toSQL(ctx)
	if err != nil {
		return err
	}
	executor, err := db.FromContext(ctx)
	if err != nil {
		return err
	}
	rows, err := executor.QueryContext(ctx, sql, arguments...)
	if err != nil {
		return fmt.Errorf("query: %w\n%s", err, sql)
	}
	defer rows.Close()
	for rows.Next() {
		object, err := scanObject(q.info, rows)
		if err != nil {
			return err
		}
		if err := fn(object); err != nil {
			return err
		}
	}
	return rows.Err()
}

// All returns every matching object as a pointer to the model struct.
func (q Dynamic) All(ctx context.Context) ([]any, error) {
	var objects []any
	err := q.Iterate(ctx, func(object any) error {
		objects = append(objects, object)
		return nil
	})
	return objects, err
}

// Get returns exactly one object matching the extra conditions.
func (q Dynamic) Get(ctx context.Context, conds ...Cond) (any, error) {
	objects, err := q.Filter(conds...).Limit(2).All(ctx)
	if err != nil {
		return nil, err
	}
	switch len(objects) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return objects[0], nil
	}
	return nil, ErrMultipleObjects
}

// GetByPK returns the object with the given primary key.
func (q Dynamic) GetByPK(ctx context.Context, primaryKey int64) (any, error) {
	return q.Get(ctx, FieldLookup(q.info.PK, "exact", primaryKey))
}

// First returns the first object in order, or ErrNotFound.
func (q Dynamic) First(ctx context.Context) (any, error) {
	objects, err := q.Limit(1).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, ErrNotFound
	}
	return objects[0], nil
}

// Count returns the number of matching rows.
func (q Dynamic) Count(ctx context.Context) (int64, error) {
	c := newCompiler(ctx)
	inner, err := q.selectSQL(c, "1", false)
	if err != nil {
		return 0, err
	}
	sql := "SELECT count(*) FROM (" + inner + ") AS \"counted\""
	executor, err := db.FromContext(ctx)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := executor.QueryRowContext(ctx, sql, c.arguments...).Scan(&count); err != nil {
		return 0, fmt.Errorf("query: %w\n%s", err, sql)
	}
	return count, nil
}

// Exists reports whether any row matches.
func (q Dynamic) Exists(ctx context.Context) (bool, error) {
	c := newCompiler(ctx)
	inner, err := q.Limit(1).selectSQL(c, "1", false)
	if err != nil {
		return false, err
	}
	executor, err := db.FromContext(ctx)
	if err != nil {
		return false, err
	}
	var exists bool
	if err := executor.QueryRowContext(ctx, "SELECT EXISTS ("+inner+")", c.arguments...).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// PrimaryKeys returns the primary keys of matching rows in order.
func (q Dynamic) PrimaryKeys(ctx context.Context) ([]int64, error) {
	c := newCompiler(ctx)
	sql, err := q.selectSQL(c, column("t0", q.info.PK), true)
	if err != nil {
		return nil, err
	}
	executor, err := db.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := executor.QueryContext(ctx, sql, c.arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []int64
	for rows.Next() {
		var key int64
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// Values returns maps of the named fields for matching rows.
func (q Dynamic) Values(ctx context.Context, names ...string) ([]map[string]any, error) {
	fields := make([]*models.Field, 0, len(names))
	columns := make([]string, 0, len(names))
	for _, name := range names {
		field := q.info.FieldByName(name)
		if field == nil {
			return nil, fmt.Errorf("query: %s has no field %q", q.info.Name, name)
		}
		fields = append(fields, field)
		columns = append(columns, column("t0", field))
	}
	c := newCompiler(ctx)
	sql, err := q.selectSQL(c, strings.Join(columns, ", "), true)
	if err != nil {
		return nil, err
	}
	executor, err := db.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := executor.QueryContext(ctx, sql, c.arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		object := q.info.New()
		if err := scanFields(rows, object, fields); err != nil {
			return nil, fmt.Errorf("query: scanning %s: %w", q.info.Name, err)
		}
		result := make(map[string]any, len(fields))
		for _, field := range fields {
			result[field.LookupName()] = field.Value(object)
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func (q Dynamic) targetSubquery(c *compiler) (string, error) {
	inner, err := q.selectSQL(c, column("t0", q.info.PK), q.limit > 0 || q.offset > 0)
	if err != nil {
		return "", err
	}
	return QuoteIdentifier(q.info.PK.Column) + " IN (" + inner + ")", nil
}

// Delete removes matching rows and returns how many were deleted.
func (q Dynamic) Delete(ctx context.Context) (int64, error) {
	c := newCompiler(ctx)
	where, err := q.targetSubquery(c)
	if err != nil {
		return 0, err
	}
	executor, err := db.FromContext(ctx)
	if err != nil {
		return 0, err
	}
	result, err := executor.ExecContext(ctx, "DELETE FROM "+QuoteIdentifier(q.info.Table)+" WHERE "+where, c.arguments...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// UpdateFields sets the given fields on matching rows (updated_at is refreshed).
func (q Dynamic) UpdateFields(ctx context.Context, assignments map[*models.Field]any) (int64, error) {
	if len(assignments) == 0 {
		return 0, nil
	}
	c := newCompiler(ctx)
	fields := make([]*models.Field, 0, len(assignments))
	for field := range assignments {
		if field.Model != q.info {
			return 0, fmt.Errorf("query: cannot update %s on a %s queryset", field, q.info.Name)
		}
		fields = append(fields, field)
	}
	slices.SortFunc(fields, func(a, b *models.Field) int { return strings.Compare(a.Column, b.Column) })
	setters := make([]string, 0, len(fields)+1)
	for _, field := range fields {
		setters = append(setters, QuoteIdentifier(field.Column)+" = "+c.placeholder(field.ToDatabase(assignments[field])))
	}
	if updatedAt := q.info.FieldByName("updated_at"); updatedAt != nil && assignments[updatedAt] == nil {
		setters = append(setters, QuoteIdentifier(updatedAt.Column)+" = "+c.dialect.Now())
	}
	where, err := q.targetSubquery(c)
	if err != nil {
		return 0, err
	}
	executor, err := db.FromContext(ctx)
	if err != nil {
		return 0, err
	}
	sql := "UPDATE " + QuoteIdentifier(q.info.Table) + " SET " + strings.Join(setters, ", ") + " WHERE " + where
	result, err := executor.ExecContext(ctx, sql, c.arguments...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// PreloadRelated loads the targets of a foreign key for objects in one query and caches them.
func PreloadRelated(ctx context.Context, objects []any, field *models.Field) error {
	if field.Kind != models.KindForeignKey {
		return fmt.Errorf("query: %s is not a foreign key", field)
	}
	related := field.Related()
	if related == nil {
		return fmt.Errorf("query: target of %s is not registered", field)
	}
	ids := map[int64]bool{}
	for _, object := range objects {
		if id, ok := field.Get(object).(int64); ok {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	values := make([]any, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	loaded, err := ForModel(related).Filter(FieldLookup(related.PK, "in", values...)).All(ctx)
	if err != nil {
		return err
	}
	byID := make(map[int64]any, len(loaded))
	for _, object := range loaded {
		byID[related.PKOf(object)] = object
	}
	for _, object := range objects {
		id, ok := field.Get(object).(int64)
		if !ok || byID[id] == nil {
			continue
		}
		field.Addr(object).Addr().MethodByName("Set").Call(reflectArguments(byID[id]))
	}
	return nil
}
