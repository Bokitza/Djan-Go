ARG GO_VERSION=1
FROM golang:${GO_VERSION}-bookworm as builder

WORKDIR /usr/src/app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go build -v -o /run-app ./example


FROM debian:bookworm

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /run-app /usr/local/bin/
COPY example/migrations ./migrations
# migrate restores the SQLite database from DATABASE_BACKUP_URL when the volume is empty and
# uploads its changes before exiting, so runserver starts on an up-to-date file.
CMD ["sh", "-c", "run-app migrate && exec run-app runserver"]
