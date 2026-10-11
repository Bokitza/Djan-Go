// Package management implements the project command line: check, makemigrations, migrate,
// showmigrations, createsuperuser and runserver.
//
//	func main() {
//		management.Run(management.App{Admin: site, Routes: routes})
//	}
package management

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"

	"github.com/yoav-rogat/djan-go/admin"
	"github.com/yoav-rogat/djan-go/auth"
	"github.com/yoav-rogat/djan-go/db"
	"github.com/yoav-rogat/djan-go/migrations"
	"github.com/yoav-rogat/djan-go/models"
)

// App describes a project.
type App struct {
	// DatabaseURL defaults to the DATABASE_URL environment variable.
	DatabaseURL string
	// MigrationsDir is where project migrations live (default "migrations").
	MigrationsDir string
	// Addr is the default runserver address (default ":8000", or the PORT environment variable).
	Addr string
	// Admin is mounted at its prefix by runserver. Optional.
	Admin *admin.Site
	// Routes registers the project's own handlers. Optional.
	Routes func(mux *http.ServeMux)
	// Commands adds project commands. They run with the default database connected.
	Commands []Command
	// Middleware wraps every request after the session user is loaded (auth.CurrentUser works). Optional.
	Middleware func(http.Handler) http.Handler

	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// Command is a project-specific command such as "seed".
type Command struct {
	Name, Usage, Help string
	Run               func(ctx context.Context, arguments []string) error
}

type command struct {
	name, usage, help string
	run               func(app *App, ctx context.Context, arguments []string) error
}

var commands = []command{
	{"check", "", "Validate models and admin configuration.", (*App).check},
	{"makemigrations", "[-name NAME] [-dry-run]", "Create a migration from model changes.", (*App).makeMigrations},
	{"migrate", "[-rollback N]", "Apply pending migrations, or roll back the last N project migrations.", (*App).migrate},
	{"showmigrations", "", "List migrations and whether they are applied.", (*App).showMigrations},
	{"createsuperuser", "[-username U] [-email E] [-password P]", "Create an admin superuser. The password may also come from DJAN_SUPERUSER_PASSWORD.", (*App).createSuperuser},
	{"runserver", "[-addr ADDR]", "Run the development server.", (*App).runServer},
}

// Run executes the command named in os.Args and exits on failure.
func Run(app App) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := app.Execute(ctx, os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(app.stderr(), "error:", err)
		os.Exit(1)
	}
}

// Execute runs one command with its arguments.
func (app *App) Execute(ctx context.Context, arguments []string) error {
	app.applyDefaults()
	if len(arguments) == 0 || arguments[0] == "help" || arguments[0] == "-h" || arguments[0] == "--help" {
		app.printUsage()
		return nil
	}
	for _, command := range commands {
		if command.name == arguments[0] {
			return command.run(app, ctx, arguments[1:])
		}
	}
	for _, command := range app.Commands {
		if command.Name == arguments[0] {
			pool, err := app.connect(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()
			return command.Run(ctx, arguments[1:])
		}
	}
	app.printUsage()
	return fmt.Errorf("unknown command %q", arguments[0])
}

func (app *App) applyDefaults() {
	if app.DatabaseURL == "" {
		app.DatabaseURL = os.Getenv("DATABASE_URL")
	}
	if app.MigrationsDir == "" {
		app.MigrationsDir = "migrations"
	}
	if app.Addr == "" {
		app.Addr = ":8000"
		if port := os.Getenv("PORT"); port != "" {
			app.Addr = ":" + port
		}
	}
	if app.Stdout == nil {
		app.Stdout = os.Stdout
	}
	if app.Stdin == nil {
		app.Stdin = os.Stdin
	}
}

func (app *App) stderr() io.Writer {
	if app.Stderr == nil {
		return os.Stderr
	}
	return app.Stderr
}

func (app *App) printUsage() {
	fmt.Fprintf(app.Stdout, "Usage: %s <command> [flags]\n\nCommands:\n", programName())
	for _, command := range commands {
		fmt.Fprintf(app.Stdout, "  %-16s %s\n  %-16s   %s\n", command.name, command.usage, "", command.help)
	}
	for _, command := range app.Commands {
		fmt.Fprintf(app.Stdout, "  %-16s %s\n  %-16s   %s\n", command.Name, command.Usage, "", command.Help)
	}
}

func programName() string {
	name := os.Args[0]
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	return name
}

func (app *App) flags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(app.stderr())
	return flags
}

func (app *App) sources() []migrations.Source { return migrations.ProjectSources(app.MigrationsDir) }

func (app *App) connect(ctx context.Context) (*pgxpool.Pool, error) {
	if app.DatabaseURL == "" {
		return nil, errors.New("no database configured: set DATABASE_URL or App.DatabaseURL")
	}
	pool, err := db.Connect(ctx, app.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	db.SetDefault(pool)
	return pool, nil
}

func (app *App) validate() error {
	problems := []error{models.Validate()}
	if app.Admin != nil {
		problems = append(problems, app.Admin.Check())
	}
	return errors.Join(problems...)
}

func (app *App) check(ctx context.Context, arguments []string) error {
	if err := app.flags("check").Parse(arguments); err != nil {
		return err
	}
	if err := app.validate(); err != nil {
		return err
	}
	fmt.Fprintf(app.Stdout, "System check found no issues (%d models).\n", len(models.All()))
	return nil
}

func (app *App) makeMigrations(ctx context.Context, arguments []string) error {
	flags := app.flags("makemigrations")
	name := flags.String("name", "", "migration name (default: initial or auto_<timestamp>)")
	dryRun := flags.Bool("dry-run", false, "print the migration instead of writing it")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := models.Validate(); err != nil {
		return err
	}
	changed := false
	for _, source := range app.sources() {
		writable := source.Directory != ""
		migration, filePath, err := migrations.MakeMigration(source, *name, writable && !*dryRun)
		if err != nil {
			return err
		}
		if migration == nil {
			continue
		}
		if !writable {
			return fmt.Errorf("the built-in %s migrations don't match its models; regenerate them in the framework", source.Name)
		}
		changed = true
		if *dryRun {
			fmt.Fprintf(app.Stdout, "Migration %s (dry run):\n", migration.Name)
		} else {
			fmt.Fprintf(app.Stdout, "Created %s:\n", filePath)
		}
		for _, operation := range migration.Operations {
			fmt.Fprintf(app.Stdout, "  - %s\n", operation.Describe())
		}
		if *dryRun {
			for _, statement := range migration.Up {
				fmt.Fprintf(app.Stdout, "\n%s;\n", statement)
			}
		}
	}
	if !changed {
		fmt.Fprintln(app.Stdout, "No changes detected.")
	}
	return nil
}

func (app *App) migrate(ctx context.Context, arguments []string) error {
	flags := app.flags("migrate")
	rollback := flags.Int("rollback", 0, "unapply the last N project migrations")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := models.Validate(); err != nil {
		return err
	}
	pool, err := app.connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	sources := app.sources()
	if *rollback > 0 {
		fmt.Fprintln(app.Stdout, "Rolling back migrations:")
		count, err := migrations.Rollback(ctx, pool, sources[len(sources)-1], *rollback, app.Stdout)
		if err == nil && count == 0 {
			fmt.Fprintln(app.Stdout, "  No migrations to roll back.")
		}
		return err
	}
	fmt.Fprintln(app.Stdout, "Running migrations:")
	count, err := migrations.Migrate(ctx, pool, sources, app.Stdout)
	if err != nil {
		return err
	}
	if count == 0 {
		fmt.Fprintln(app.Stdout, "  No migrations to apply.")
	}
	for _, source := range sources {
		if source.Directory == "" {
			continue
		}
		if migration, _, err := migrations.MakeMigration(source, "", false); err == nil && migration != nil {
			fmt.Fprintln(app.Stdout, "\nYour models have changes that are not reflected in a migration. Run 'makemigrations'.")
		}
	}
	return nil
}

func (app *App) showMigrations(ctx context.Context, arguments []string) error {
	if err := app.flags("showmigrations").Parse(arguments); err != nil {
		return err
	}
	pool, err := app.connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	statuses, err := migrations.ShowStatus(ctx, pool, app.sources())
	if err != nil {
		return err
	}
	current := ""
	for _, status := range statuses {
		if status.Source != current {
			current = status.Source
			fmt.Fprintln(app.Stdout, current)
		}
		mark := " "
		if status.Applied {
			mark = "X"
		}
		fmt.Fprintf(app.Stdout, " [%s] %s\n", mark, status.Name)
	}
	if len(statuses) == 0 {
		fmt.Fprintln(app.Stdout, "No migrations.")
	}
	return nil
}

func (app *App) createSuperuser(ctx context.Context, arguments []string) error {
	flags := app.flags("createsuperuser")
	username := flags.String("username", "", "username")
	email := flags.String("email", "", "email address")
	password := flags.String("password", "", "password (prefer DJAN_SUPERUSER_PASSWORD or the prompt)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *password == "" {
		*password = os.Getenv("DJAN_SUPERUSER_PASSWORD")
	}
	reader := bufio.NewReader(app.Stdin)
	var err error
	if *username == "" {
		if *username, err = app.prompt(reader, "Username: "); err != nil {
			return err
		}
	}
	if *email == "" && *password == "" {
		if *email, err = app.prompt(reader, "Email address (optional): "); err != nil {
			return err
		}
	}
	if *password == "" {
		if *password, err = app.promptPassword(reader); err != nil {
			return err
		}
	}
	if len(*password) < 8 {
		return errors.New("the password must be at least 8 characters")
	}
	pool, err := app.connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	user, err := auth.CreateSuperuser(ctx, *username, *email, *password)
	if err != nil {
		return err
	}
	fmt.Fprintf(app.Stdout, "Superuser %q created.\n", user.Username)
	return nil
}

func (app *App) prompt(reader *bufio.Reader, label string) (string, error) {
	fmt.Fprint(app.Stdout, label)
	line, err := reader.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func (app *App) promptPassword(reader *bufio.Reader) (string, error) {
	read := func(label string) (string, error) {
		if file, ok := app.Stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			fmt.Fprint(app.Stdout, label)
			secret, err := term.ReadPassword(int(file.Fd()))
			fmt.Fprintln(app.Stdout)
			return string(secret), err
		}
		return app.prompt(reader, label)
	}
	password, err := read("Password: ")
	if err != nil {
		return "", err
	}
	confirmation, err := read("Password (again): ")
	if err != nil {
		return "", err
	}
	if password != confirmation {
		return "", errors.New("the passwords don't match")
	}
	return password, nil
}

// Handler builds the HTTP handler served by runserver: the admin (if any) plus the project's
// routes, with the session user loaded for every request.
func (app *App) Handler() http.Handler {
	mux := http.NewServeMux()
	if app.Admin != nil {
		mux.Handle(strings.TrimSuffix(app.Admin.Prefix, "/")+"/", app.Admin.Handler())
	}
	if app.Routes != nil {
		app.Routes(mux)
	} else if app.Admin != nil {
		mux.Handle("GET /{$}", http.RedirectHandler(strings.TrimSuffix(app.Admin.Prefix, "/")+"/", http.StatusFound))
	}
	if app.Middleware != nil {
		return auth.Middleware(app.Middleware(mux))
	}
	return auth.Middleware(mux)
}

func (app *App) runServer(ctx context.Context, arguments []string) error {
	flags := app.flags("runserver")
	address := flags.String("addr", app.Addr, "listen address")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := app.validate(); err != nil {
		return err
	}
	pool, err := app.connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	if pending, err := migrations.Pending(ctx, pool, app.sources()); err == nil && pending > 0 {
		fmt.Fprintf(app.Stdout, "You have %d unapplied migration(s). Run 'migrate' to apply them.\n", pending)
	}

	server := &http.Server{Addr: *address, Handler: logRequests(app.Stdout, app.Handler()), ReadHeaderTimeout: 10 * time.Second}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	display := *address
	if strings.HasPrefix(display, ":") {
		display = "localhost" + display
	}
	fmt.Fprintf(app.Stdout, "Serving on http://%s/", display)
	if app.Admin != nil {
		fmt.Fprintf(app.Stdout, " (admin at http://%s%s/)", display, strings.TrimSuffix(app.Admin.Prefix, "/"))
	}
	fmt.Fprintln(app.Stdout, "\nQuit with CONTROL-C.")

	select {
	case err := <-serverErrors:
		return err
	case <-ctx.Done():
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdownContext)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Unwrap() http.ResponseWriter { return recorder.ResponseWriter }

func logRequests(output io.Writer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		fmt.Fprintf(output, "[%s] %s %s %d %s\n", started.Format("15:04:05"), r.Method, r.URL.RequestURI(), recorder.status, time.Since(started).Round(time.Millisecond))
	})
}
