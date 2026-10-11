// Command authmigrations regenerates the built-in auth migrations after auth models change.
// Run it from the repository root: go run ./internal/tools/authmigrations
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/yoav/djan-go/auth"
	"github.com/yoav/djan-go/migrations"
)

func main() {
	source := auth.MigrationSource()
	source.FS = os.DirFS("auth/migrations")
	source.Directory = "auth/migrations"
	migration, path, err := migrations.MakeMigration(source, "", true)
	if err != nil {
		log.Fatal(err)
	}
	if migration == nil {
		fmt.Println("No changes detected in auth models")
		return
	}
	fmt.Println("Wrote", path)
}
