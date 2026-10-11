# djan-go

[![CI](https://github.com/yoav-rogat/djan-go/actions/workflows/ci.yml/badge.svg)](https://github.com/yoav-rogat/djan-go/actions/workflows/ci.yml)

> [!WARNING]
> **This project is a work in progress.** APIs change without notice, features are missing,
> and nothing here has been hardened for production. Use it to explore ideas, not to ship.

djan-go is a Django-inspired web framework for Go, built on Postgres. It aims to bring
Django's batteries-included productivity — declarative models, an ORM, automatic migrations,
an auto-generated admin, auth and management commands — to Go, while leaning on generics so
that field references are checked by the compiler instead of being passed around as strings.

## Features

- **Models** (`models`): plain Go structs with a `Schema` method that declares fields, constraints,
  ordering, choices, defaults, foreign keys and lifecycle hooks (`Validate`, `BeforeSave`).
- **Typed query API** (`query`): `query.Objects[Post]().Where(...)` with conditions built from
  pointers to struct fields, so typos and type mismatches fail at compile time. A string-based
  `query.Q{"author__name__icontains": "yo"}` fallback exists for dynamic filters.
- **Migrations** (`migrations`): autodetects model changes and writes JSON migration files,
  with apply and rollback support.
- **Admin** (`admin`): an auto-generated admin site (HTMX + Alpine + Tailwind, all embedded) with
  list displays, search, filters, forms and per-user permission hooks.
- **Auth** (`auth`): users, password hashing, sessions and permissions, with built-in migrations.
- **Management commands** (`management`): `check`, `makemigrations`, `migrate`,
  `showmigrations`, `createsuperuser`, `runserver`, plus your own custom commands.

## A taste

```go
type Post struct {
	models.Model
	Title  string
	Status string
	Author models.FK[Author]
}

func (p *Post) Schema(s *models.Schema) {
	s.Char(&p.Title, 200)
	s.Char(&p.Status, 20).Choices("draft", "published").Default("draft").Index()
	models.ForeignKey(s, &p.Author).OnDelete(models.Protect)
}

func init() { models.Register[Post]() }

// Elsewhere:
posts, err := query.Objects[Post]().
	Where(func(p *Post) query.Cond { return query.Eq(&p.Status, "published") }).
	OrderBy(func(p *Post) []query.Order { return []query.Order{query.Desc(&p.CreatedAt)} }).
	All(ctx)
```

Wiring an app together:

```go
func main() {
	site := admin.NewSite("Blog admin")
	blog.RegisterAdmin(site)

	management.Run(management.App{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Admin:       site,
		Routes:      blog.Routes,
	})
}
```

## Running the example

The [`example`](example) directory contains a small blog (authors, posts, tags, comments) with
an admin and a JSON API. You need Go (see `go.mod` for the version) and Docker.

```sh
make db                    # start Postgres via docker compose
cd example
go run . migrate
go run . createsuperuser
go run . seed              # optional demo data
go run . runserver
```

Then open http://localhost:8000/admin/ (the port can be changed with `-addr` or `PORT`).

## Development

```sh
make test         # all tests, including Postgres integration tests
make test-short   # unit tests only
make css          # rebuild the admin stylesheet with Tailwind
```

Integration tests use the database at `DJAN_TEST_DATABASE_URL` when set; otherwise they start a
throwaway Postgres container with testcontainers (and are skipped if Docker isn't available).
Each test gets its own fresh database.

## Project layout

| Path | Contents |
| --- | --- |
| `models/` | Model declarations, schema DSL and registry |
| `query/` | Typed and dynamic query sets, SQL compilation, persistence |
| `migrations/` | Migration autodetection, files and runner |
| `admin/` | Admin site, views, templates and static assets |
| `auth/` | Users, passwords, sessions, permissions |
| `management/` | Command-line entry point for apps |
| `db/` | Connection pool plumbing via `context` |
| `example/` | Example blog app |
