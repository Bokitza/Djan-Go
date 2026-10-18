// Package db manages database connections and transactions for every supported backend.
//
// A backend is an Adapter chosen by the scheme of the database URL. The postgres and sqlite
// subpackages register themselves when imported:
//
//	import (
//		_ "github.com/Bokitza/djan-go/db/postgres"
//		_ "github.com/Bokitza/djan-go/db/sqlite"
//	)
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Executor is implemented by *sql.DB, *sql.Conn and *sql.Tx.
type Executor interface {
	ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row
}

// Adapter is an open database of one backend.
type Adapter interface {
	// Name is the backend name: "postgres" or "sqlite".
	Name() string
	Database() *sql.DB
	Dialect() Dialect
	// Close closes the connections. The sqlite adapter first uploads the last changes to the backup.
	Close(ctx context.Context) error
}

// Options configure Open.
type Options struct {
	// BackupURL is where the sqlite adapter streams continuous backups, e.g.
	// s3://bucket/path?endpoint=https://fly.storage.tigris.dev. Empty disables backups.
	BackupURL string
}

// Opener opens an adapter for a database URL.
type Opener func(ctx context.Context, databaseURL string, options Options) (Adapter, error)

// ErrNoDatabase is returned when no database was configured.
var ErrNoDatabase = errors.New("db: no database connection configured; call db.Connect first")

var (
	openersLock sync.RWMutex
	openers     = map[string]Opener{}

	defaultAdapterLock sync.RWMutex
	defaultAdapter     Adapter
)

// Register makes an adapter available for the given URL schemes.
func Register(opener Opener, schemes ...string) {
	openersLock.Lock()
	defer openersLock.Unlock()
	for _, scheme := range schemes {
		openers[scheme] = opener
	}
}

// Open opens the adapter registered for the scheme of databaseURL and verifies the connection.
func Open(ctx context.Context, databaseURL string, options Options) (Adapter, error) {
	scheme, _, found := strings.Cut(databaseURL, ":")
	if !found || scheme == "" {
		return nil, fmt.Errorf("db: database URL %q has no scheme (expected postgres://, sqlite:// or file:)", databaseURL)
	}
	openersLock.RLock()
	opener, ok := openers[strings.ToLower(scheme)]
	registered := make([]string, 0, len(openers))
	for name := range openers {
		registered = append(registered, name)
	}
	openersLock.RUnlock()
	if !ok {
		sort.Strings(registered)
		return nil, fmt.Errorf("db: no adapter registered for %q URLs (registered: %s); import github.com/Bokitza/djan-go/db/postgres or db/sqlite",
			scheme, strings.Join(registered, ", "))
	}
	return opener(ctx, databaseURL, options)
}

// Connect opens a database without backups and makes it the default adapter.
func Connect(ctx context.Context, databaseURL string) (Adapter, error) {
	adapter, err := Open(ctx, databaseURL, Options{})
	if err != nil {
		return nil, err
	}
	SetDefault(adapter)
	return adapter, nil
}

// SetDefault replaces the default adapter used when the context carries none.
func SetDefault(adapter Adapter) {
	defaultAdapterLock.Lock()
	defer defaultAdapterLock.Unlock()
	defaultAdapter = adapter
}

// Default returns the default adapter, or nil.
func Default() Adapter {
	defaultAdapterLock.RLock()
	defer defaultAdapterLock.RUnlock()
	return defaultAdapter
}

type adapterKey struct{}
type transactionKey struct{}

type transaction struct {
	transaction *sql.Tx
	dialect     Dialect
	savepoints  int
}

// WithAdapter returns a context whose queries use the given adapter instead of the default one.
func WithAdapter(ctx context.Context, adapter Adapter) context.Context {
	return context.WithValue(ctx, adapterKey{}, adapter)
}

// AdapterFromContext returns the adapter attached to the context, falling back to the default adapter.
func AdapterFromContext(ctx context.Context) (Adapter, error) {
	if adapter, ok := ctx.Value(adapterKey{}).(Adapter); ok && adapter != nil {
		return adapter, nil
	}
	if adapter := Default(); adapter != nil {
		return adapter, nil
	}
	return nil, ErrNoDatabase
}

// DialectFromContext returns the dialect of the active transaction or adapter.
// Without any database it returns Postgres, so SQL can still be rendered (e.g. by QuerySet.ToSQL).
func DialectFromContext(ctx context.Context) Dialect {
	if active, ok := ctx.Value(transactionKey{}).(*transaction); ok {
		return active.dialect
	}
	if adapter, err := AdapterFromContext(ctx); err == nil {
		return adapter.Dialect()
	}
	return Postgres
}

// FromContext returns the active transaction if there is one, otherwise the adapter's database.
func FromContext(ctx context.Context) (Executor, error) {
	if active, ok := ctx.Value(transactionKey{}).(*transaction); ok {
		return active.transaction, nil
	}
	adapter, err := AdapterFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return adapter.Database(), nil
}

// Atomic runs fn inside a transaction stored in the context. Nested calls use savepoints.
func Atomic(ctx context.Context, fn func(ctx context.Context) error) error {
	if active, ok := ctx.Value(transactionKey{}).(*transaction); ok {
		return active.savepoint(ctx, fn)
	}
	adapter, err := AdapterFromContext(ctx)
	if err != nil {
		return err
	}
	started, err := adapter.Database().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = started.Rollback()
			panic(recovered)
		}
	}()
	active := &transaction{transaction: started, dialect: adapter.Dialect()}
	if err := fn(context.WithValue(ctx, transactionKey{}, active)); err != nil {
		if rollbackErr := started.Rollback(); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return started.Commit()
}

func (t *transaction) savepoint(ctx context.Context, fn func(ctx context.Context) error) error {
	t.savepoints++
	name := "djan_savepoint_" + strconv.Itoa(t.savepoints)
	if _, err := t.transaction.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return err
	}
	rollback := func() error {
		if _, err := t.transaction.ExecContext(context.WithoutCancel(ctx), "ROLLBACK TO SAVEPOINT "+name); err != nil {
			return err
		}
		_, err := t.transaction.ExecContext(context.WithoutCancel(ctx), "RELEASE SAVEPOINT "+name)
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = rollback()
			panic(recovered)
		}
	}()
	if err := fn(ctx); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	_, err := t.transaction.ExecContext(ctx, "RELEASE SAVEPOINT "+name)
	return err
}
