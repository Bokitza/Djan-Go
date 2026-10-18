// Package sqlite registers the SQLite adapter for sqlite:// and file: URLs.
//
// It uses the pure-Go modernc.org/sqlite driver, so no CGO is needed. When a backup URL is given,
// an embedded Litestream restores the database from it if the local file is missing, then streams
// every change to it until the adapter is closed. Backup URLs are those Litestream understands,
// e.g. s3://bucket/path?endpoint=https://fly.storage.tigris.dev (credentials come from
// AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY) or file:///var/backups/app for a local directory.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/benbjohnson/litestream"
	_ "github.com/benbjohnson/litestream/file"
	_ "github.com/benbjohnson/litestream/s3"
	_ "modernc.org/sqlite"

	"github.com/Bokitza/djan-go/db"
)

func init() { db.Register(Open, "sqlite", "sqlite3", "file") }

// connectionPragmas keep SQLite safe for a concurrent web server: WAL lets readers run beside the
// writer, busy_timeout waits for the write lock instead of failing, and BEGIN IMMEDIATE takes that
// lock up front so transactions don't fail when upgrading from read to write.
var connectionPragmas = []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"}

type adapter struct {
	database *sql.DB
	path     string
	backup   *litestream.Store
}

// Open opens (creating if needed) the SQLite database file named by databaseURL, such as
// sqlite:///data/app.db, sqlite://app.db (relative) or file:/data/app.db. Query parameters are
// passed to the driver.
func Open(ctx context.Context, databaseURL string, options db.Options) (db.Adapter, error) {
	path, parameters, err := parseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	if directory := filepath.Dir(path); directory != "" {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("db: creating the directory of %s: %w", path, err)
		}
	}
	var client litestream.ReplicaClient
	if options.BackupURL != "" {
		if client, err = litestream.NewReplicaClientFromURL(options.BackupURL); err != nil {
			return nil, fmt.Errorf("db: DATABASE_BACKUP_URL: %w", err)
		}
		if err := restoreIfMissing(ctx, client, path, options.BackupURL); err != nil {
			return nil, err
		}
	}

	for _, pragma := range connectionPragmas {
		parameters.Add("_pragma", pragma)
	}
	parameters.Set("_txlock", "immediate")
	parameters.Set("_time_format", "sqlite")
	database, err := sql.Open("sqlite", "file:"+path+"?"+parameters.Encode())
	if err != nil {
		return nil, err
	}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("db: opening %s: %w", path, err)
	}
	opened := &adapter{database: database, path: path}
	if client == nil {
		return opened, nil
	}

	replicated := litestream.NewDB(path)
	replicated.Replica = litestream.NewReplicaWithClient(replicated, client)
	opened.backup = litestream.NewStore([]*litestream.DB{replicated}, litestream.DefaultCompactionLevels)
	if err := opened.backup.Open(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("db: starting the backup to %s: %w", redact(options.BackupURL), err)
	}
	// Litestream attaches to the database on its first monitor tick, and Close only uploads after
	// that; syncing now makes short commands such as migrate back up their changes too.
	if err := replicated.Sync(ctx); err != nil {
		_ = opened.Close(ctx)
		return nil, fmt.Errorf("db: starting the backup to %s: %w", redact(options.BackupURL), err)
	}
	slog.Info("database backup enabled", "database", path, "backup", redact(options.BackupURL))
	return opened, nil
}

func (a *adapter) Name() string        { return "sqlite" }
func (a *adapter) Database() *sql.DB   { return a.database }
func (a *adapter) Dialect() db.Dialect { return db.SQLite }

// Close closes the connections, then uploads the remaining changes to the backup.
func (a *adapter) Close(ctx context.Context) error {
	err := a.database.Close()
	if a.backup != nil {
		if backupErr := a.backup.Close(ctx); backupErr != nil {
			err = errors.Join(err, fmt.Errorf("db: final backup of %s: %w", a.path, backupErr))
		}
	}
	return err
}

func parseURL(databaseURL string) (string, url.Values, error) {
	_, rest, _ := strings.Cut(databaseURL, ":")
	rest, query, _ := strings.Cut(rest, "?")
	parameters, err := url.ParseQuery(query)
	if err != nil {
		return "", nil, fmt.Errorf("db: invalid sqlite URL parameters: %w", err)
	}
	path := strings.TrimPrefix(rest, "//")
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	if path == "" || strings.HasPrefix(path, ":memory:") || parameters.Get("mode") == "memory" {
		return "", nil, fmt.Errorf("db: sqlite URL %q must name a database file (in-memory databases are not supported)", databaseURL)
	}
	return path, parameters, nil
}

func restoreIfMissing(ctx context.Context, client litestream.ReplicaClient, path, backupURL string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := client.Init(ctx); err != nil {
		return fmt.Errorf("db: connecting to the backup at %s: %w", redact(backupURL), err)
	}
	options := litestream.NewRestoreOptions()
	options.OutputPath = path
	err := litestream.NewReplicaWithClient(nil, client).Restore(ctx, options)
	switch {
	case errors.Is(err, litestream.ErrNoSnapshots), errors.Is(err, litestream.ErrTxNotAvailable):
		slog.Info("no database backup found, starting with an empty database", "database", path, "backup", redact(backupURL))
		return nil
	case err != nil:
		return fmt.Errorf("db: restoring %s from %s: %w", path, redact(backupURL), err)
	}
	slog.Info("database restored from backup", "database", path, "backup", redact(backupURL))
	return nil
}

// redact hides credentials that may be embedded in a backup URL.
func redact(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "(invalid URL)"
	}
	return parsed.Redacted()
}
