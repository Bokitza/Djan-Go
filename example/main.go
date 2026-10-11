// Command example is a small blog built with djan-go.
//
//	docker compose up -d
//	go run . makemigrations
//	go run . migrate
//	go run . createsuperuser
//	go run . seed
//	go run . runserver
package main

import (
	"os"

	"github.com/yoav-rogat/djan-go/admin"
	"github.com/yoav-rogat/djan-go/example/blog"
	"github.com/yoav-rogat/djan-go/management"
)

func main() {
	site := admin.NewSite("Blog admin")
	blog.RegisterAdmin(site)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://djan:djan@localhost:5432/djan?sslmode=disable"
	}
	management.Run(management.App{
		DatabaseURL: databaseURL,
		Admin:       site,
		Routes:      blog.Routes,
		Commands: []management.Command{
			{Name: "seed", Help: "Create demo authors, posts, comments and two editor accounts.", Run: blog.Seed},
		},
	})
}
