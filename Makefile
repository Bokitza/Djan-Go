TAILWIND_VERSION ?= v4.1.14
TAILWIND := bin/tailwindcss
UNAME_S := $(shell uname -s | tr A-Z a-z)
UNAME_M := $(shell uname -m | sed -e 's/x86_64/x64/' -e 's/aarch64/arm64/')
TAILWIND_PLATFORM := $(subst darwin,macos,$(UNAME_S))-$(UNAME_M)

.PHONY: css vendor test test-short example db auth-migrations

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

# Integration tests use DJAN_TEST_DATABASE_URL when set, otherwise testcontainers (Docker).
test:
	go test ./...

test-short:
	go test -short ./...

db:
	docker compose up -d postgres

# Regenerate the built-in auth migrations after changing auth models.
auth-migrations:
	go run ./internal/tools/authmigrations

example:
	cd example && go run . migrate && go run . runserver
