// Package auth provides users, groups, permissions and cookie sessions stored in Postgres.
package auth

import (
	"embed"
	"io/fs"
	"time"

	"github.com/yoav-rogat/djan-go/migrations"
	"github.com/yoav-rogat/djan-go/models"
)

// User is an account. Staff users may log into the admin; superusers have every permission.
type User struct {
	models.Model
	Username    string
	Email       string
	FirstName   string
	LastName    string
	Password    string
	IsActive    bool
	IsStaff     bool
	IsSuperuser bool
	LastLogin   *time.Time

	permissionCache map[string]bool
}

func (u *User) Schema(s *models.Schema) {
	s.App("auth")
	s.Table("auth_user")
	s.Ordering(models.Asc(&u.Username))
	s.Char(&u.Username, 150).Unique().Help("Letters, digits and @/./+/-/_ only.")
	s.Char(&u.Email, 254).Blank().Verbose("Email address")
	s.Char(&u.FirstName, 150).Blank()
	s.Char(&u.LastName, 150).Blank()
	s.Char(&u.Password, 255).ReadOnly()
	s.Bool(&u.IsActive).Default(true).Verbose("Active").Help("Unselect this instead of deleting accounts.")
	s.Bool(&u.IsStaff).Default(false).Verbose("Staff status").Help("Can log into the admin site.")
	s.Bool(&u.IsSuperuser).Default(false).Verbose("Superuser status").Help("Has all permissions without assigning them.")
	s.NullableDateTime(&u.LastLogin).ReadOnly()
}

func (u *User) String() string { return u.Username }

// Group is a named collection of permissions.
type Group struct {
	models.Model
	Name string
}

func (g *Group) Schema(s *models.Schema) {
	s.App("auth")
	s.Table("auth_group")
	s.Ordering(models.Asc(&g.Name))
	s.Char(&g.Name, 150).Unique()
}

func (g *Group) String() string { return g.Name }

// Permission is "<app>.<codename>", e.g. "blog.change_post".
type Permission struct {
	models.Model
	Name     string
	App      string
	Codename string
}

func (p *Permission) Schema(s *models.Schema) {
	s.App("auth")
	s.Table("auth_permission")
	s.Ordering(models.Asc(&p.App), models.Asc(&p.Codename))
	s.Char(&p.Name, 255)
	s.Char(&p.App, 100)
	s.Char(&p.Codename, 100)
	s.UniqueTogether(&p.App, &p.Codename)
}

func (p *Permission) String() string { return p.App + "." + p.Codename }

// UserGroup links users and groups.
type UserGroup struct {
	models.Model
	User  models.FK[User]
	Group models.FK[Group]
}

func (m *UserGroup) Schema(s *models.Schema) {
	s.App("auth")
	s.Table("auth_user_groups")
	models.ForeignKey(s, &m.User)
	models.ForeignKey(s, &m.Group)
	s.UniqueTogether(&m.User, &m.Group)
}

// UserPermission grants a permission directly to a user.
type UserPermission struct {
	models.Model
	User       models.FK[User]
	Permission models.FK[Permission]
}

func (m *UserPermission) Schema(s *models.Schema) {
	s.App("auth")
	s.Table("auth_user_permissions")
	models.ForeignKey(s, &m.User)
	models.ForeignKey(s, &m.Permission)
	s.UniqueTogether(&m.User, &m.Permission)
}

// GroupPermission grants a permission to every member of a group.
type GroupPermission struct {
	models.Model
	Group      models.FK[Group]
	Permission models.FK[Permission]
}

func (m *GroupPermission) Schema(s *models.Schema) {
	s.App("auth")
	s.Table("auth_group_permissions")
	models.ForeignKey(s, &m.Group)
	models.ForeignKey(s, &m.Permission)
	s.UniqueTogether(&m.Group, &m.Permission)
}

// Session is a login session; Key stores the SHA-256 of the cookie value.
type Session struct {
	models.Model
	Key       string
	User      models.FK[User]
	ExpiresAt time.Time
}

func (m *Session) Schema(s *models.Schema) {
	s.App("auth")
	s.Table("auth_session")
	s.Char(&m.Key, 64).Unique()
	models.ForeignKey(s, &m.User)
	s.DateTime(&m.ExpiresAt).Index()
}

//go:embed all:migrations
var migrationFiles embed.FS

// MigrationSource is the built-in migration source for the auth tables.
func MigrationSource() migrations.Source {
	files, _ := fs.Sub(migrationFiles, "migrations")
	return migrations.Source{
		Name: "auth",
		FS:   files,
		Owns: func(info *models.ModelInfo) bool { return info.App == "auth" },
	}
}

func init() {
	models.Register[User]()
	models.Register[Group]()
	models.Register[Permission]()
	models.Register[UserGroup]()
	models.Register[UserPermission]()
	models.Register[GroupPermission]()
	models.Register[Session]()
	migrations.RegisterSource(MigrationSource())
	migrations.OnPostMigrate(CreatePermissions)
}
