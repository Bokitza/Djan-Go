package blog_test

import (
	"testing"

	"github.com/yoav-rogat/djan-go/admin"
	"github.com/yoav-rogat/djan-go/example/blog"
	"github.com/yoav-rogat/djan-go/migrations"
	"github.com/yoav-rogat/djan-go/models"
)

func TestModelsAndAdminAreValid(t *testing.T) {
	models.MustValidate(t)
	site := admin.NewSite("test")
	blog.RegisterAdmin(site)
	if err := site.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsAreUpToDate(t *testing.T) {
	for _, source := range migrations.ProjectSources("../migrations") {
		migration, _, err := migrations.MakeMigration(source, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if migration != nil {
			t.Fatalf("%s migrations are missing changes; run 'go run . makemigrations' in example/", source.Name)
		}
	}
}
