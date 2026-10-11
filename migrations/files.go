package migrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yoav-rogat/djan-go/models"
)

// Migration is the content of one migration file.
type Migration struct {
	Name       string      `json:"name"`
	Operations []Operation `json:"operations"`
	Up         []string    `json:"up"`
	Down       []string    `json:"down"`
}

// Source is a set of migrations owning some models: the built-in auth app, or the project.
type Source struct {
	Name      string
	FS        fs.FS                        // where migration files are read from
	Directory string                       // where makemigrations writes (empty for embedded sources)
	Owns      func(*models.ModelInfo) bool // which models this source manages
}

var (
	frameworkSourcesLock sync.Mutex
	frameworkSources     []Source
)

// RegisterSource registers a built-in migration source (used by framework apps such as auth).
func RegisterSource(source Source) {
	frameworkSourcesLock.Lock()
	defer frameworkSourcesLock.Unlock()
	frameworkSources = append(frameworkSources, source)
}

// ProjectSources returns the framework sources followed by the project source stored in directory,
// which owns every model not owned by a framework source.
func ProjectSources(directory string) []Source {
	frameworkSourcesLock.Lock()
	sources := append([]Source(nil), frameworkSources...)
	frameworkSourcesLock.Unlock()
	framework := append([]Source(nil), sources...)
	return append(sources, Source{
		Name:      "project",
		FS:        os.DirFS(directory),
		Directory: directory,
		Owns: func(info *models.ModelInfo) bool {
			for _, source := range framework {
				if source.Owns(info) {
					return false
				}
			}
			return true
		},
	})
}

// Load reads the migrations of a source in name order. A missing directory means no migrations.
func Load(source Source) ([]*Migration, error) {
	entries, err := fs.ReadDir(source.FS, ".")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	migrations := make([]*Migration, 0, len(names))
	for _, name := range names {
		content, err := fs.ReadFile(source.FS, name)
		if err != nil {
			return nil, err
		}
		migration := &Migration{}
		if err := json.Unmarshal(content, migration); err != nil {
			return nil, fmt.Errorf("migrations: %s/%s: %w", source.Name, name, err)
		}
		migration.Name = strings.TrimSuffix(path.Base(name), ".json")
		migrations = append(migrations, migration)
	}
	return migrations, nil
}

// Replay builds the state described by migrations.
func Replay(migrations []*Migration) (*State, error) {
	state := NewState()
	for _, migration := range migrations {
		for _, operation := range migration.Operations {
			if err := operation.Apply(state); err != nil {
				return nil, fmt.Errorf("%s: %w", migration.Name, err)
			}
		}
	}
	return state, nil
}

// OwnedModels returns the registered models managed by source.
func OwnedModels(source Source) []*models.ModelInfo {
	var owned []*models.ModelInfo
	for _, info := range models.All() {
		if source.Owns(info) {
			owned = append(owned, info)
		}
	}
	return owned
}

// NewMigration builds a migration from operations, generating its SQL.
func NewMigration(name string, operations []Operation) *Migration {
	migration := &Migration{Name: name, Operations: operations}
	for _, operation := range operations {
		migration.Up = append(migration.Up, operation.Up()...)
	}
	for index := len(operations) - 1; index >= 0; index-- {
		migration.Down = append(migration.Down, operations[index].Down()...)
	}
	return migration
}

// MakeMigration diffs the source's migrations against its models. It returns nil when nothing changed.
// When write is true the migration is saved as <directory>/<NNNN>_<name>.json.
func MakeMigration(source Source, name string, write bool) (*Migration, string, error) {
	existing, err := Load(source)
	if err != nil {
		return nil, "", err
	}
	current, err := Replay(existing)
	if err != nil {
		return nil, "", err
	}
	operations := Diff(current, TargetState(OwnedModels(source)))
	if len(operations) == 0 {
		return nil, "", nil
	}
	if name == "" {
		name = "auto_" + time.Now().Format("20060102_1504")
		if len(existing) == 0 {
			name = "initial"
		}
	}
	migration := NewMigration(fmt.Sprintf("%04d_%s", len(existing)+1, name), operations)
	if !write {
		return migration, "", nil
	}
	if source.Directory == "" {
		return nil, "", fmt.Errorf("migrations: source %s is read-only", source.Name)
	}
	if err := os.MkdirAll(source.Directory, 0o755); err != nil {
		return nil, "", err
	}
	encoded, err := json.MarshalIndent(migration, "", "  ")
	if err != nil {
		return nil, "", err
	}
	filePath := filepath.Join(source.Directory, migration.Name+".json")
	return migration, filePath, os.WriteFile(filePath, append(encoded, '\n'), 0o644)
}
