package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/yoav-rogat/djan-go/db"
	"github.com/yoav-rogat/djan-go/models"
	"github.com/yoav-rogat/djan-go/query"
)

// Actions that get an automatic permission for every model.
var Actions = []string{"add", "change", "delete", "view"}

// PermissionCodename returns "<app>.<action>_<model>", e.g. "blog.change_post".
func PermissionCodename(info *models.ModelInfo, action string) string {
	return info.App + "." + action + "_" + info.Key()
}

// CreatePermissions creates the add/change/delete/view permissions of every registered model.
// It runs automatically after migrate.
func CreatePermissions(ctx context.Context) error {
	executor, err := db.FromContext(ctx)
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, info := range models.All() {
		for _, action := range Actions {
			batch.Queue(`INSERT INTO auth_permission (name, app, codename) VALUES ($1, $2, $3) ON CONFLICT (app, codename) DO NOTHING`,
				fmt.Sprintf("Can %s %s", action, strings.ToLower(info.VerboseName)), info.App, action+"_"+info.Key())
		}
	}
	type batchSender interface {
		SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults
	}
	sender, ok := executor.(batchSender)
	if !ok {
		return errors.New("auth: executor cannot send batches")
	}
	return sender.SendBatch(ctx, batch).Close()
}

const userPermissionsSQL = `SELECT p.app || '.' || p.codename FROM auth_permission p
WHERE p.id IN (SELECT permission_id FROM auth_user_permissions WHERE user_id = $1)
   OR p.id IN (SELECT gp.permission_id FROM auth_group_permissions gp
               JOIN auth_user_groups ug ON ug.group_id = gp.group_id WHERE ug.user_id = $1)`

// Permissions returns every permission of the user (direct and through groups), cached per object.
func (u *User) Permissions(ctx context.Context) (map[string]bool, error) {
	if u.permissionCache != nil {
		return u.permissionCache, nil
	}
	executor, err := db.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := executor.Query(ctx, userPermissionsSQL, u.ID)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	u.permissionCache = make(map[string]bool, len(names))
	for _, name := range names {
		u.permissionCache[name] = true
	}
	return u.permissionCache, nil
}

// HasPerm reports whether the user has the permission ("blog.change_post").
// Superusers have every permission; inactive users have none.
func (u *User) HasPerm(ctx context.Context, permission string) bool {
	if u == nil || !u.IsActive {
		return false
	}
	if u.IsSuperuser {
		return true
	}
	permissions, err := u.Permissions(ctx)
	return err == nil && permissions[permission]
}

// HasModelPerm checks the automatic permission of a model action ("add", "change", "delete", "view").
func (u *User) HasModelPerm(ctx context.Context, info *models.ModelInfo, action string) bool {
	return u.HasPerm(ctx, PermissionCodename(info, action))
}

// ForgetPermissions clears the permission cache, e.g. after changing group membership.
func (u *User) ForgetPermissions() { u.permissionCache = nil }

// GrantPermission gives a user a permission by its "<app>.<codename>" name.
func GrantPermission(ctx context.Context, user *User, permission string) error {
	found, err := findPermission(ctx, permission)
	if err != nil {
		return err
	}
	user.ForgetPermissions()
	return query.Create(ctx, &UserPermission{User: models.RefTo(user), Permission: models.RefTo(found)})
}

// GrantGroupPermission gives a group a permission by its "<app>.<codename>" name.
func GrantGroupPermission(ctx context.Context, group *Group, permission string) error {
	found, err := findPermission(ctx, permission)
	if err != nil {
		return err
	}
	return query.Create(ctx, &GroupPermission{Group: models.RefTo(group), Permission: models.RefTo(found)})
}

// AddToGroup adds a user to a group.
func AddToGroup(ctx context.Context, user *User, group *Group) error {
	user.ForgetPermissions()
	return query.Create(ctx, &UserGroup{User: models.RefTo(user), Group: models.RefTo(group)})
}

func findPermission(ctx context.Context, permission string) (*Permission, error) {
	app, codename, ok := strings.Cut(permission, ".")
	if !ok {
		return nil, fmt.Errorf("auth: permission %q must look like <app>.<codename>", permission)
	}
	found, err := query.Objects[Permission]().Get(ctx, func(p *Permission) query.Cond {
		return query.And(query.Eq(&p.App, app), query.Eq(&p.Codename, codename))
	})
	if err != nil {
		return nil, fmt.Errorf("auth: permission %q: %w", permission, err)
	}
	return found, nil
}
