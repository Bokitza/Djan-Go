// Package postgres registers the Postgres adapter for postgres:// and postgresql:// URLs.
package postgres

import (
	"context"
	"database/sql"
	"errors"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Bokitza/djan-go/db"
)

func init() { db.Register(Open, "postgres", "postgresql") }

type adapter struct{ database *sql.DB }

// Open connects to Postgres. Backups are the job of the database host, so options.BackupURL is rejected.
func Open(ctx context.Context, databaseURL string, options db.Options) (db.Adapter, error) {
	if options.BackupURL != "" {
		return nil, errors.New("db: DATABASE_BACKUP_URL only applies to sqlite databases; back up Postgres with your database host")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return &adapter{database: database}, nil
}

func (a *adapter) Name() string                { return "postgres" }
func (a *adapter) Database() *sql.DB           { return a.database }
func (a *adapter) Dialect() db.Dialect         { return db.Postgres }
func (a *adapter) Close(context.Context) error { return a.database.Close() }
