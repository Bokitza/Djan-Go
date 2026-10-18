# djan-go

![djan-go](docs/banner.jpg)

[![CI](https://github.com/Bokitza/djan-go/actions/workflows/ci.yml/badge.svg)](https://github.com/Bokitza/djan-go/actions/workflows/ci.yml)

> [!WARNING]
> **This project is a work in progress.** APIs change without notice, features are missing,
> and nothing here has been hardened for production. Use it to explore ideas, not to ship.

djan-go is a Django-inspired web framework for Go, running on Postgres or SQLite. It aims to bring
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
- **Import/export** (`importexport`): django-import-export style resources that move model data
  to and from CSV, TSV and JSON, with custom headers, foreign key lookups by name, computed
  columns, skip-unchanged, dry runs and per-row error reports, all inside a transaction. Each
  `ModelAdmin` opts in with an `ImportExport` policy that adds import (upload, preview, confirm),
  export of the filtered list and export of selected rows, following the admin's permission rules.
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

Import and export are configured per model in its admin, much like django-import-export's
`ImportExportModelAdmin` with a `resource_class`:

```go
admin.Register(site, admin.ModelAdmin[Post]{
	ImportExport: &admin.ImportExport[Post]{
		Import: true, Export: true, ExportSelected: true,
		Resources: []importexport.Resource[Post]{{
			Configure: func(p *Post, config *importexport.Config) {
				config.Fields(
					importexport.F(&p.Slug),
					importexport.F(&p.Title),
					importexport.F(&p.Author).Widget(importexport.ForeignKey(func(a *Author) *string { return &a.Name })),
					importexport.Through(&p.Author, func(a *Author) importexport.Field { return importexport.F(&a.Email) }),
				)
				config.ImportIDFields(importexport.F(&p.Slug))
				config.SkipUnchanged()
			},
			BeforeImportRow: func(ctx context.Context, row importexport.Row) error {
				user := admin.RequestFrom(ctx).User // the request, like kwargs["request"]
				...
			},
		}},
	},
})
```

Use `ResourcesFor(r)` instead of `Resources` to choose resources per user (returning none hides
import and export), like overriding `get_resource_classes`.

Text fields can be edited with a WYSIWYG editor ([Tiptap](https://tiptap.dev/), embedded) instead
of a plain textarea. The field stores HTML, which is sanitized with an allowlist when the form is
saved; read-only forms render the HTML and list columns show its plain text:

```go
admin.Register(site, admin.ModelAdmin[Post]{
	Configure: func(p *Post, config *admin.Config) {
		config.RichText(&p.Body)
	},
})
```

## Databases

The scheme of `DATABASE_URL` picks the backend:

| `DATABASE_URL` | Backend |
| --- | --- |
| `postgres://user:pass@host:5432/app` | Postgres, through pgx |
| `sqlite:///data/app.db` (or `file:/data/app.db`, `sqlite://app.db` for a relative path) | A local SQLite file, through the pure-Go `modernc.org/sqlite` driver (no CGO) |

The same models, queries, migration files and admin work on both. Migration files keep their
Postgres SQL for review; on SQLite the runner renders the SQL from the file's operations instead,
rebuilding a table when SQLite's `ALTER TABLE` can't make a change (column type or nullability,
foreign keys).

SQLite databases can be backed up continuously by setting `DATABASE_BACKUP_URL`. An embedded
[Litestream](https://litestream.io) streams every change to the backup while the app runs, and
when the database file is missing at startup (a new machine, a lost volume) it is first restored
from the backup:

```sh
DATABASE_URL=sqlite:///data/app.db
DATABASE_BACKUP_URL=s3://my-bucket/app?endpoint=https://fly.storage.tigris.dev&region=auto
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
```

Any S3-compatible storage works (AWS S3, Tigris, Cloudflare R2, Backblaze B2, MinIO), and
`file:///path/to/directory` backs up to a local directory. Setting `DATABASE_BACKUP_URL` with
Postgres is an error: back up Postgres with your database host.

Things to know before choosing SQLite:

- One machine, one writer. The database is a file on one machine's disk; writes are serialized
  (readers run alongside thanks to WAL mode). Don't scale the app to several machines.
- Backups are asynchronous: a hard crash can lose about the last second of writes. Commands
  upload their remaining changes when they exit, including on `SIGTERM`.
- Case-insensitive lookups (`IContains`, `IExact`, ...) fold only ASCII letters, as SQLite's
  `LIKE` and `lower()` do. Languages without letter case, such as Hebrew, are unaffected.
- Decimals are stored with numeric affinity, so values beyond about 15 significant digits lose
  precision.

### Deploying on Fly.io with SQLite

The included [`fly.toml`](fly.toml) mounts a volume at `/data`, and the [`Dockerfile`](Dockerfile)
runs `migrate` before `runserver` on the machine itself, because a release machine has no volume.
To set up backups to [Tigris](https://fly.io/docs/tigris/):

```sh
fly volumes create djan_data --size 1 --region fra
fly storage create                  # creates a bucket and sets the AWS_* secrets
fly secrets set DATABASE_BACKUP_URL="s3://<bucket>/app?endpoint=https://fly.storage.tigris.dev&region=auto"
fly deploy
fly scale count 1                   # SQLite: exactly one machine
```

## Running the example

The [`example`](example) directory contains a small blog (authors, posts, tags, comments) with
an admin and a JSON API. You need Go (see `go.mod` for the version), and Docker for Postgres.

```sh
make db                    # start Postgres via docker compose
                           # or skip Docker: export DATABASE_URL=sqlite://blog.db
cd example
go run . migrate
go run . createsuperuser
go run . seed              # optional demo data
go run . runserver
```

Then open http://localhost:8000/admin/ (the port can be changed with `-addr` or `PORT`).

## Development

```sh
make test           # all tests; integration tests run on temporary SQLite databases
make test-postgres  # the same tests on the docker compose Postgres (start it with `make db`)
make test-short     # skip the tests that need Docker
make css            # rebuild the admin stylesheet with Tailwind
make vendor-tiptap  # rebundle the embedded Tiptap editor (needs Node.js)
```

Each integration test gets its own fresh database: a SQLite file by default, or a new database
on the Postgres server named by `DJAN_TEST_DATABASE_URL` (set it to `docker` to start a throwaway
Postgres container with testcontainers). CI runs the suite on both.

## Project layout

| Path | Contents |
| --- | --- |
| `models/` | Model declarations, schema DSL and registry |
| `query/` | Typed and dynamic query sets, SQL compilation, persistence |
| `migrations/` | Migration autodetection, files and runner |
| `admin/` | Admin site, views, templates and static assets |
| `auth/` | Users, passwords, sessions, permissions |
| `importexport/` | Resources, widgets and CSV/TSV/JSON formats for data import and export |
| `management/` | Command-line entry point for apps |
| `db/` | Database adapters (`db/postgres`, `db/sqlite` with Litestream backups), dialects, transactions via `context` |
| `example/` | Example blog app |
