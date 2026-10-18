TAILWIND_VERSION ?= v4.1.14
TAILWIND := bin/tailwindcss
UNAME_S := $(shell uname -s | tr A-Z a-z)
UNAME_M := $(shell uname -m | sed -e 's/x86_64/x64/' -e 's/aarch64/arm64/')
TAILWIND_PLATFORM := $(subst darwin,macos,$(UNAME_S))-$(UNAME_M)

TIPTAP_VERSION ?= 3

.PHONY: css vendor vendor-tiptap test test-short test-postgres example db auth-migrations

$(TAILWIND):
	mkdir -p bin
	curl -sSL -o $(TAILWIND) https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TAILWIND_PLATFORM)
	chmod +x $(TAILWIND)

# Rebuild the embedded admin stylesheet.
css: $(TAILWIND)
	$(TAILWIND) -i admin/assets/admin.css -o admin/static/admin.css --minify

# Re-download the vendored admin JavaScript.
vendor:
	curl -sSL -o admin/static/htmx.min.js https://unpkg.com/htmx.org@2.0.7/dist/htmx.min.js
	curl -sSL -o admin/static/alpine.min.js https://unpkg.com/alpinejs@3.15.0/dist/cdn.min.js

# Rebundle the vendored Tiptap editor (needs Node.js).
vendor-tiptap:
	mkdir -p bin/tiptap
	npm install --silent --no-save --prefix bin/tiptap @tiptap/core@$(TIPTAP_VERSION) @tiptap/starter-kit@$(TIPTAP_VERSION) @tiptap/pm@$(TIPTAP_VERSION) esbuild
	NODE_PATH=bin/tiptap/node_modules bin/tiptap/node_modules/.bin/esbuild admin/assets/tiptap-entry.js \
		--bundle --minify --format=esm --legal-comments=none --outfile=admin/static/tiptap.min.js

# Integration tests run on temporary SQLite files unless DJAN_TEST_DATABASE_URL names a Postgres
# server (or is "docker" to start one with testcontainers). -short skips tests that need Docker.
test:
	go test ./...

test-short:
	go test -short ./...

# Run the integration tests on the docker compose Postgres (see `make db`).
test-postgres:
	DJAN_TEST_DATABASE_URL=postgres://djan:djan@localhost:5432/djan?sslmode=disable go test ./...

db:
	docker compose up -d postgres

# Regenerate the built-in auth migrations after changing auth models.
auth-migrations:
	go run ./internal/tools/authmigrations

example:
	cd example && go run . migrate && go run . runserver
