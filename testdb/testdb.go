// Package testdb gives integration tests an isolated database.
//
// By default every call creates a fresh SQLite database file in the test's temporary directory, so
// no server or Docker is needed. Set DJAN_TEST_DATABASE_URL to a Postgres URL to run the same tests
// on Postgres (a fresh database is created on that server and dropped when the test ends), or to
// "docker" to start a Postgres container with testcontainers-go.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Bokitza/djan-go/db"
	_ "github.com/Bokitza/djan-go/db/postgres"
	_ "github.com/Bokitza/djan-go/db/sqlite"
	"github.com/Bokitza/djan-go/migrations"
	"github.com/Bokitza/djan-go/models"
)

// Database is an isolated test database.
type Database struct {
	db.Adapter
	// URL connects to the same database, e.g. for management.App.DatabaseURL.
	URL string
}

var (
	serverOnce sync.Once
	serverURL  string
	serverErr  error
)

func postgresServer(configured string) (string, error) {
	serverOnce.Do(func() {
		if configured != "docker" {
			serverURL = configured
			return
		}
		ctx := context.Background()
		container, err := postgrescontainer.Run(ctx, "postgres:17-alpine",
			postgrescontainer.WithDatabase("djan"), postgrescontainer.WithUsername("djan"), postgrescontainer.WithPassword("djan"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
		if err != nil {
			serverErr = err
			return
		}
		serverURL, serverErr = container.ConnectionString(ctx, "sslmode=disable")
	})
	return serverURL, serverErr
}

// New creates a fresh database and returns a context bound to it.
func New(t testing.TB) (context.Context, *Database) {
	t.Helper()
	configured := os.Getenv("DJAN_TEST_DATABASE_URL")
	databaseURL := "sqlite://" + filepath.Join(t.TempDir(), "test.db")
	if configured != "" && !strings.HasPrefix(configured, "sqlite") && !strings.HasPrefix(configured, "file:") {
		databaseURL = newPostgresDatabase(t, configured)
	}
	ctx := context.Background()
	adapter, err := db.Open(ctx, databaseURL, db.Options{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.Background()) })
	return db.WithAdapter(ctx, adapter), &Database{Adapter: adapter, URL: databaseURL}
}

func newPostgresDatabase(t testing.TB, configured string) string {
	t.Helper()
	address, err := func() (address string, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("%v", recovered)
			}
		}()
		return postgresServer(configured)
	}()
	if err != nil {
		t.Skipf("no Postgres available (start Docker or set DJAN_TEST_DATABASE_URL to a server): %v", err)
	}
	admin, err := sql.Open("pgx", address)
	if err != nil {
		t.Fatalf("connect to test server: %v", err)
	}
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	name := "djan_test_" + hex.EncodeToString(random)
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
		_ = admin.Close()
	})
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	return parsed.String()
}

// CreateTables creates tables for every registered model directly from their declarations.
func CreateTables(t testing.TB, ctx context.Context) {
	t.Helper()
	executor, err := db.FromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	migration := migrations.NewMigration("test", migrations.Diff(migrations.NewState(), migrations.TargetState(models.All())))
	statements, err := migration.SQL(db.DialectFromContext(ctx), migrations.NewState(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := executor.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%v\n%s", err, statement)
		}
	}
}
