package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Bokitza/djan-go/db"
	_ "github.com/Bokitza/djan-go/db/postgres"
)

func TestBackupURLIsRejected(t *testing.T) {
	_, err := db.Open(context.Background(), "postgres://localhost/app", db.Options{BackupURL: "s3://bucket/app"})
	if err == nil || !strings.Contains(err.Error(), "only applies to sqlite") {
		t.Fatalf("expected a clear error, got %v", err)
	}
}
