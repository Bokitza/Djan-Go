// Package testdb gives integration tests an isolated Postgres database.
//
// It uses DJAN_TEST_DATABASE_URL when set; otherwise it starts a Postgres container with
// testcontainers-go (skipping the test when Docker is unavailable). Every call creates a fresh
// database that is dropped when the test ends.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/yoav/djan-go/db"
	"github.com/yoav/djan-go/migrations"
	"github.com/yoav/djan-go/models"
)

var (
	serverOnce sync.Once
	serverURL  string
	serverErr  error
)

func server() (string, error) {
	serverOnce.Do(func() {
		if configured := os.Getenv("DJAN_TEST_DATABASE_URL"); configured != "" {
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

// New creates a fresh database and returns a context bound to its pool.
func New(t testing.TB) (context.Context, *pgxpool.Pool) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	address, err := func() (address string, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("%v", recovered)
			}
		}()
		return server()
	}()
	if err != nil {
		t.Skipf("no Postgres available (set DJAN_TEST_DATABASE_URL or start Docker): %v", err)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatalf("connect to test server: %v", err)
	}
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	name := "djan_test_" + hex.EncodeToString(random)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	return db.WithPool(ctx, pool), pool
}

// CreateTables creates tables for every registered model directly from their declarations.
func CreateTables(t testing.TB, ctx context.Context) {
	t.Helper()
	executor, err := db.FromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	migration := migrations.NewMigration("test", migrations.Diff(migrations.NewState(), migrations.TargetState(models.All())))
	for _, statement := range migration.Up {
		if _, err := executor.Exec(ctx, statement); err != nil {
			t.Fatalf("%v\n%s", err, statement)
		}
	}
}
