// Package db manages the Postgres connection pool and transactions.
package db

import (
	"context"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Executor is implemented by both *pgxpool.Pool and pgx.Tx.
type Executor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

// ErrNoDatabase is returned when no pool was configured.
var ErrNoDatabase = errors.New("db: no database connection configured; call db.Connect first")

var (
	defaultPoolLock sync.RWMutex
	defaultPool     *pgxpool.Pool
)

type transactionKey struct{}
type poolKey struct{}

// Connect opens a connection pool, verifies it and makes it the default pool.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	SetDefault(pool)
	return pool, nil
}

// SetDefault replaces the default pool used when the context carries none.
func SetDefault(pool *pgxpool.Pool) {
	defaultPoolLock.Lock()
	defer defaultPoolLock.Unlock()
	defaultPool = pool
}

// Default returns the default pool, or nil.
func Default() *pgxpool.Pool {
	defaultPoolLock.RLock()
	defer defaultPoolLock.RUnlock()
	return defaultPool
}

// WithPool returns a context whose queries use the given pool instead of the default one.
func WithPool(ctx context.Context, pool *pgxpool.Pool) context.Context {
	return context.WithValue(ctx, poolKey{}, pool)
}

// PoolFromContext returns the pool attached to the context, falling back to the default pool.
func PoolFromContext(ctx context.Context) (*pgxpool.Pool, error) {
	if pool, ok := ctx.Value(poolKey{}).(*pgxpool.Pool); ok && pool != nil {
		return pool, nil
	}
	if pool := Default(); pool != nil {
		return pool, nil
	}
	return nil, ErrNoDatabase
}

// FromContext returns the active transaction if there is one, otherwise the pool.
func FromContext(ctx context.Context) (Executor, error) {
	if transaction, ok := ctx.Value(transactionKey{}).(pgx.Tx); ok {
		return transaction, nil
	}
	return PoolFromContext(ctx)
}

// Atomic runs fn inside a transaction stored in the context. Nested calls use savepoints.
func Atomic(ctx context.Context, fn func(ctx context.Context) error) error {
	var transaction pgx.Tx
	var err error
	if outer, ok := ctx.Value(transactionKey{}).(pgx.Tx); ok {
		transaction, err = outer.Begin(ctx)
	} else {
		pool, poolErr := PoolFromContext(ctx)
		if poolErr != nil {
			return poolErr
		}
		transaction, err = pool.Begin(ctx)
	}
	if err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = transaction.Rollback(ctx)
			panic(recovered)
		}
	}()
	if err := fn(context.WithValue(ctx, transactionKey{}, transaction)); err != nil {
		if rollbackErr := transaction.Rollback(ctx); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return transaction.Commit(ctx)
}
