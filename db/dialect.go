package db

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

// Dialect describes the SQL differences between backends.
//
// Column types are written in their canonical (Postgres) spelling everywhere in the framework, as
// returned by models.Field.DatabaseType and stored in migration files; ColumnType translates them.
type Dialect interface {
	Name() string
	// Placeholder returns the bind parameter for the 1-based argument index.
	Placeholder(index int) string
	// Now is the SQL expression for the current timestamp.
	Now() string
	// ColumnType translates a canonical column type such as "timestamptz" or "varchar(200)".
	ColumnType(canonical string) string
	// Constraint classifies a constraint violation error.
	Constraint(err error) (Constraint, bool)
	// LockMigrations serializes migration runs across processes, until unlock is called.
	LockMigrations(ctx context.Context, connection *sql.Conn) (unlock func(), err error)
}

// ConstraintKind is the kind of a violated database constraint.
type ConstraintKind int

const (
	UniqueViolation ConstraintKind = iota + 1
	ForeignKeyViolation
	// InvalidValue covers NOT NULL, CHECK and value-too-long errors.
	InvalidValue
)

// Constraint describes a constraint violation reported by the database.
type Constraint struct {
	Kind ConstraintKind
	// Detail is the database's description, e.g. `Key (email)=(a@b.c) already exists.`.
	Detail string
}

// ConstraintViolation classifies err with every known dialect.
func ConstraintViolation(err error) (Constraint, bool) {
	for _, dialect := range []Dialect{Postgres, SQLite} {
		if constraint, ok := dialect.Constraint(err); ok {
			return constraint, true
		}
	}
	return Constraint{}, false
}

// IsUniqueViolation reports whether err is a unique constraint violation.
func IsUniqueViolation(err error) bool {
	constraint, ok := ConstraintViolation(err)
	return ok && constraint.Kind == UniqueViolation
}

var (
	// Postgres is the dialect of the postgres adapter.
	Postgres Dialect = postgresDialect{}
	// SQLite is the dialect of the sqlite adapter.
	SQLite Dialect = sqliteDialect{}
)

type postgresDialect struct{}

func (postgresDialect) Name() string                       { return "postgres" }
func (postgresDialect) Placeholder(index int) string       { return "$" + strconv.Itoa(index) }
func (postgresDialect) Now() string                        { return "now()" }
func (postgresDialect) ColumnType(canonical string) string { return canonical }

func (postgresDialect) Constraint(err error) (Constraint, bool) {
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) {
		return Constraint{}, false
	}
	detail := strings.TrimSpace(databaseError.Detail)
	switch databaseError.Code {
	case "23505":
		return Constraint{Kind: UniqueViolation, Detail: detail}, true
	case "23503":
		return Constraint{Kind: ForeignKeyViolation, Detail: detail}, true
	case "23502", "23514", "22001":
		return Constraint{Kind: InvalidValue, Detail: databaseError.Message}, true
	}
	return Constraint{}, false
}

const postgresMigrationLockKey = 7_316_204_801

func (postgresDialect) LockMigrations(ctx context.Context, connection *sql.Conn) (func(), error) {
	if _, err := connection.ExecContext(ctx, "SELECT pg_advisory_lock($1)", int64(postgresMigrationLockKey)); err != nil {
		return nil, err
	}
	return func() {
		_, _ = connection.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", int64(postgresMigrationLockKey))
	}, nil
}

type sqliteDialect struct{}

func (sqliteDialect) Name() string                 { return "sqlite" }
func (sqliteDialect) Placeholder(index int) string { return "?" + strconv.Itoa(index) }

// Now matches the format the sqlite adapter writes time.Time values in, so timestamps sort as text.
func (sqliteDialect) Now() string { return "strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')" }

var sqliteColumnTypes = map[string]string{
	"bigint":           "INTEGER",
	"integer":          "INTEGER",
	"text":             "TEXT",
	"double precision": "REAL",
	"boolean":          "BOOLEAN",
	"timestamptz":      "DATETIME",
	"date":             "DATE",
	"bytea":            "BLOB",
	"uuid":             "TEXT",
	"jsonb":            "TEXT",
}

func (sqliteDialect) ColumnType(canonical string) string {
	if translated, ok := sqliteColumnTypes[canonical]; ok {
		return translated
	}
	if size, ok := strings.CutPrefix(canonical, "varchar"); ok {
		return "VARCHAR" + size
	}
	if precision, ok := strings.CutPrefix(canonical, "numeric"); ok {
		return "DECIMAL" + precision
	}
	return canonical
}

// SQLite extended result codes, see https://www.sqlite.org/rescode.html.
const (
	sqliteConstraintCheck      = 275
	sqliteConstraintForeignKey = 787
	sqliteConstraintNotNull    = 1299
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

var sqliteErrorPrefix = regexp.MustCompile(`^(constraint failed: )?|\s*\(\d+\)$`)

func (sqliteDialect) Constraint(err error) (Constraint, bool) {
	var coded interface{ Code() int }
	if !errors.As(err, &coded) {
		return Constraint{}, false
	}
	detail := sqliteErrorPrefix.ReplaceAllString(err.Error(), "")
	switch coded.Code() {
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return Constraint{Kind: UniqueViolation, Detail: detail}, true
	case sqliteConstraintForeignKey:
		return Constraint{Kind: ForeignKeyViolation, Detail: detail}, true
	case sqliteConstraintNotNull, sqliteConstraintCheck:
		return Constraint{Kind: InvalidValue, Detail: detail}, true
	}
	return Constraint{}, false
}

var sqliteMigrationLock sync.Mutex

// LockMigrations only serializes runs within the process: each sqlite migration transaction starts
// with BEGIN IMMEDIATE and re-checks the history table, which covers concurrent processes.
func (sqliteDialect) LockMigrations(context.Context, *sql.Conn) (func(), error) {
	sqliteMigrationLock.Lock()
	return sqliteMigrationLock.Unlock, nil
}
