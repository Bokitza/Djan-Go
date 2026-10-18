package sqlite_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Bokitza/djan-go/db"
	_ "github.com/Bokitza/djan-go/db/sqlite"
)

func TestOpenURLs(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	for _, databaseURL := range []string{
		"sqlite://" + filepath.Join(directory, "a.db"),
		"sqlite:" + filepath.Join(directory, "b.db"),
		"file:" + filepath.Join(directory, "nested", "c.db") + "?_pragma=cache_size(-2000)",
	} {
		adapter, err := db.Open(ctx, databaseURL, db.Options{})
		if err != nil {
			t.Fatalf("%s: %v", databaseURL, err)
		}
		var mode string
		if err := adapter.Database().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("%s: journal mode %q %v", databaseURL, mode, err)
		}
		if err := adapter.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, databaseURL := range []string{"sqlite::memory:", "sqlite://"} {
		if _, err := db.Open(ctx, databaseURL, db.Options{}); err == nil {
			t.Fatalf("%s should be rejected", databaseURL)
		}
	}
	if _, err := db.Open(ctx, "mysql://localhost/app", db.Options{}); err == nil {
		t.Fatal("unregistered schemes should be rejected")
	}
}

func TestBackupToDirectory(t *testing.T) {
	roundTrip(t, "file://"+filepath.Join(t.TempDir(), "backups"))
}

// TestBackupToS3 runs the round trip against SeaweedFS, an S3-compatible server, in Docker.
func TestBackupToS3(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	ctx := context.Background()
	container, err := func() (container testcontainers.Container, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("%v", recovered)
			}
		}()
		return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        "chrislusf/seaweedfs:latest",
				Cmd:          []string{"server", "-s3", "-dir=/data"},
				ExposedPorts: []string{"8333/tcp"},
				WaitingFor:   wait.ForListeningPort("8333/tcp").WithStartupTimeout(90 * time.Second),
			},
			Started: true,
		})
	}()
	if err != nil {
		t.Skipf("no Docker available: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	endpoint, err := container.PortEndpoint(ctx, "8333/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "djan")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "djan-secret")

	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       "us-east-1",
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider("djan", "djan-secret", ""),
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("djan-backups")})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("create bucket: %v", err)
		}
		time.Sleep(time.Second)
	}
	roundTrip(t, "s3://djan-backups/app?endpoint="+endpoint+"&region=us-east-1")
}

// roundTrip writes rows with backups enabled, loses the local disk, and checks that reopening
// restores every row, twice.
func roundTrip(t *testing.T, backupURL string) {
	t.Helper()
	ctx := context.Background()
	disk := filepath.Join(t.TempDir(), "data")
	databaseURL := "sqlite://" + filepath.Join(disk, "app.db")
	open := func() db.Adapter {
		t.Helper()
		adapter, err := db.Open(ctx, databaseURL, db.Options{BackupURL: backupURL})
		if err != nil {
			t.Fatal(err)
		}
		return adapter
	}
	count := func(adapter db.Adapter) int {
		t.Helper()
		var rows int
		if err := adapter.Database().QueryRowContext(ctx, "SELECT count(*) FROM notes").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	closeAndLoseDisk := func(adapter db.Adapter) {
		t.Helper()
		if err := adapter.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(disk); err != nil {
			t.Fatal(err)
		}
	}

	adapter := open()
	err := db.Atomic(db.WithAdapter(ctx, adapter), func(ctx context.Context) error {
		executor, err := db.FromContext(ctx)
		if err != nil {
			return err
		}
		if _, err := executor.ExecContext(ctx, "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
			return err
		}
		for index := range 100 {
			if _, err := executor.ExecContext(ctx, "INSERT INTO notes (body) VALUES (?1)", fmt.Sprint("note ", index)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	closeAndLoseDisk(adapter)

	adapter = open()
	if rows := count(adapter); rows != 100 {
		t.Fatalf("restored %d rows, want 100", rows)
	}
	if _, err := adapter.Database().ExecContext(ctx, "INSERT INTO notes (body) VALUES ('after restore')"); err != nil {
		t.Fatal(err)
	}
	closeAndLoseDisk(adapter)

	adapter = open()
	defer adapter.Close(ctx)
	if rows := count(adapter); rows != 101 {
		t.Fatalf("restored %d rows after the second restore, want 101", rows)
	}
}
