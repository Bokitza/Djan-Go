package admin

import (
	"errors"
	"net/url"
	"slices"
	"strconv"

	"github.com/Bokitza/djan-go/auth"
	"github.com/Bokitza/djan-go/models"
	"github.com/Bokitza/djan-go/query"
)

// extraField is a form section that is not a model column (passwords, many-to-many links, inlines).
type extraField interface {
	name() string
	render(r *Request, object any, isNew, editable bool, submitted url.Values) (extraView, error)
	// validate runs after the submitted form was bound to object, before anything is saved.
	validate(r *Request, object any, isNew bool, form url.Values) string
	save(r *Request, object any, form url.Values) error
}

type extraOption struct {
	Value, Label string
	Checked      bool
}

type extraOptionGroup struct {
	Label   string
	Options []extraOption
}

type extraView struct {
	Name, Label, Help, Widget, Error string
	ReadOnly, Required, Hidden       bool
	Groups                           []extraOptionGroup
	Count                            int
	Inline                           *inlineView
}

func registerAuthAdmins(site *Site) {
	protectSuperusers := func(r *Request, qs query.QuerySet[auth.User]) query.QuerySet[auth.User] {
		if r.User.IsSuperuser {
			return qs
		}
		return qs.WhereNot(func(u *auth.User) query.Cond { return query.Eq(&u.IsSuperuser, true) })
	}
	Register(site, ModelAdmin[auth.User]{
		Icon: "users",
		Configure: func(u *auth.User, c *Config) {
			c.ListDisplay(F(&u.Username), F(&u.Email), F(&u.FirstName), F(&u.LastName), F(&u.IsStaff), F(&u.IsActive), F(&u.LastLogin))
			c.SearchFields(&u.Username, &u.Email, &u.FirstName, &u.LastName)
			c.ListFilter(F(&u.IsStaff), F(&u.IsSuperuser), F(&u.IsActive))
			c.Exclude(F(&u.Password))
		},
		ChangeQuerySet: protectSuperusers,
		DeleteQuerySet: func(r *Request, qs query.QuerySet[auth.User]) query.QuerySet[auth.User] {
			return protectSuperusers(r, qs).WhereNot(func(u *auth.User) query.Cond { return query.Eq(&u.ID, r.User.ID) })
		},
		BeforeSave: func(r *Request, user *auth.User, isNew bool) error {
			if r.User.IsSuperuser {
				return nil
			}
			wasSuperuser := false
			if !isNew {
				current, err := query.Objects[auth.User]().GetByPK(r.Context(), user.ID)
				if err != nil {
					return err
				}
				wasSuperuser = current.IsSuperuser
			}
			if user.IsSuperuser != wasSuperuser {
				return errors.New("only superusers can grant or revoke superuser status")
			}
			return nil
		},
		extras: []extraField{passwordExtra{}, userGroupsExtra{}, userPermissionsExtra{}},
	})
	Register(site, ModelAdmin[auth.Group]{
		Icon: "shield",
		Configure: func(g *auth.Group, c *Config) {
			c.ListDisplay(F(&g.Name), F(&g.CreatedAt))
			c.SearchFields(&g.Name)
		},
		extras: []extraField{groupPermissionsExtra{}},
	})
}

// ---- password ----

type passwordExtra struct{}

func (passwordExtra) name() string { return "new_password" }

func (passwordExtra) render(r *Request, object any, isNew, editable bool, submitted url.Values) (extraView, error) {
	view := extraView{Name: "new_password", Label: "Password", Widget: "password", Required: isNew, ReadOnly: !editable}
	if isNew {
		view.Help = "At least 8 characters."
	} else {
		view.Label = "Change password"
		view.Help = "Leave blank to keep the current password."
		if !object.(*auth.User).HasUsablePassword() {
			view.Help = "No password set. Set one to allow password logins."
		}
	}
	return view, nil
}

func (passwordExtra) validate(r *Request, _ any, isNew bool, form url.Values) string {
	password, confirmation := form.Get("new_password"), form.Get("new_password_confirm")
	switch {
	case password == "" && isNew:
		return "This field is required."
	case password == "":
		return ""
	case len(password) < 8:
		return "This password is too short. It must contain at least 8 characters."
	case password != confirmation:
		return "The two password fields didn't match."
	}
	return ""
}

func (passwordExtra) save(r *Request, object any, form url.Values) error {
	password := form.Get("new_password")
	if password == "" {
		return nil
	}
	user := object.(*auth.User)
	if err := user.SetPassword(password); err != nil {
		return err
	}
	_, err := query.Objects[auth.User]().Where(func(u *auth.User) query.Cond { return query.Eq(&u.ID, user.ID) }).
		Update(r.Context(), func(u *auth.User) []query.Assign { return []query.Assign{query.Set(&u.Password, user.Password)} })
	return err
}

// ---- many-to-many helpers ----

func selectedIDs(form url.Values, name string) []int64 {
	var ids []int64
	for _, raw := range form[name] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func permissionGroups(r *Request, checked map[int64]bool, submitted url.Values, name string) ([]extraOptionGroup, int, error) {
	permissions, err := query.Objects[auth.Permission]().All(r.Context())
	if err != nil {
		return nil, 0, err
	}
	if submitted != nil {
		checked = map[int64]bool{}
		for _, id := range selectedIDs(submitted, name) {
			checked[id] = true
		}
	}
	var groups []extraOptionGroup
	count := 0
	for _, permission := range permissions {
		label := r.Site.appLabel(permission.App)
		if len(groups) == 0 || groups[len(groups)-1].Label != label {
			groups = append(groups, extraOptionGroup{Label: label})
		}
		option := extraOption{Value: strconv.FormatInt(permission.ID, 10), Label: permission.Name, Checked: checked[permission.ID]}
		if option.Checked {
			count++
		}
		groups[len(groups)-1].Options = append(groups[len(groups)-1].Options, option)
	}
	return groups, count, nil
}

// ---- user groups ----

type userGroupsExtra struct{}

func (userGroupsExtra) name() string { return "groups" }

func (userGroupsExtra) render(r *Request, object any, isNew, editable bool, submitted url.Values) (extraView, error) {
	user := object.(*auth.User)
	view := extraView{Name: "groups", Label: "Groups", Widget: "checkboxes", ReadOnly: !editable || !r.User.IsSuperuser,
		Help: "The user gets every permission granted to these groups."}
	checked := map[int64]bool{}
	if !isNew {
		links, err := query.Objects[auth.UserGroup]().Where(func(m *auth.UserGroup) query.Cond { return query.Eq(&m.User.ID, user.ID) }).All(r.Context())
		if err != nil {
			return view, err
		}
		for _, link := range links {
			checked[link.Group.ID] = true
		}
	}
	if submitted != nil {
		checked = map[int64]bool{}
		for _, id := range selectedIDs(submitted, "groups") {
			checked[id] = true
		}
	}
	groups, err := query.Objects[auth.Group]().All(r.Context())
	if err != nil {
		return view, err
	}
	option := extraOptionGroup{}
	for _, group := range groups {
		item := extraOption{Value: strconv.FormatInt(group.ID, 10), Label: group.Name, Checked: checked[group.ID]}
		if item.Checked {
			view.Count++
		}
		option.Options = append(option.Options, item)
	}
	view.Groups = []extraOptionGroup{option}
	return view, nil
}

func (userGroupsExtra) validate(*Request, any, bool, url.Values) string { return "" }

func (userGroupsExtra) save(r *Request, object any, form url.Values) error {
	if !r.User.IsSuperuser {
		return nil
	}
	user := object.(*auth.User)
	ctx := r.Context()
	if _, err := query.Objects[auth.UserGroup]().Where(func(m *auth.UserGroup) query.Cond { return query.Eq(&m.User.ID, user.ID) }).Delete(ctx); err != nil {
		return err
	}
	for _, id := range selectedIDs(form, "groups") {
		if err := query.Create(ctx, &auth.UserGroup{User: models.Ref[auth.User](user.ID), Group: models.Ref[auth.Group](id)}); err != nil {
			return err
		}
	}
	return nil
}

// ---- user permissions ----

type userPermissionsExtra struct{}

func (userPermissionsExtra) name() string { return "user_permissions" }

func (userPermissionsExtra) render(r *Request, object any, isNew, editable bool, submitted url.Values) (extraView, error) {
	user := object.(*auth.User)
	view := extraView{Name: "user_permissions", Label: "User permissions", Widget: "checkboxes", ReadOnly: !editable || !r.User.IsSuperuser,
		Help: "Specific permissions for this user, in addition to those of their groups."}
	checked := map[int64]bool{}
	if !isNew {
		links, err := query.Objects[auth.UserPermission]().Where(func(m *auth.UserPermission) query.Cond { return query.Eq(&m.User.ID, user.ID) }).All(r.Context())
		if err != nil {
			return view, err
		}
		for _, link := range links {
			checked[link.Permission.ID] = true
		}
	}
	groups, count, err := permissionGroups(r, checked, submitted, "user_permissions")
	view.Groups, view.Count = groups, count
	return view, err
}

func (userPermissionsExtra) validate(*Request, any, bool, url.Values) string { return "" }

func (userPermissionsExtra) save(r *Request, object any, form url.Values) error {
	if !r.User.IsSuperuser {
		return nil
	}
	user := object.(*auth.User)
	ctx := r.Context()
	if _, err := query.Objects[auth.UserPermission]().Where(func(m *auth.UserPermission) query.Cond { return query.Eq(&m.User.ID, user.ID) }).Delete(ctx); err != nil {
		return err
	}
	for _, id := range selectedIDs(form, "user_permissions") {
		if err := query.Create(ctx, &auth.UserPermission{User: models.Ref[auth.User](user.ID), Permission: models.Ref[auth.Permission](id)}); err != nil {
			return err
		}
	}
	return nil
}

// ---- group permissions ----

type groupPermissionsExtra struct{}

func (groupPermissionsExtra) name() string { return "permissions" }

func (groupPermissionsExtra) render(r *Request, object any, isNew, editable bool, submitted url.Values) (extraView, error) {
	group := object.(*auth.Group)
	view := extraView{Name: "permissions", Label: "Permissions", Widget: "checkboxes", ReadOnly: !editable || !r.User.IsSuperuser}
	checked := map[int64]bool{}
	if !isNew {
		links, err := query.Objects[auth.GroupPermission]().Where(func(m *auth.GroupPermission) query.Cond { return query.Eq(&m.Group.ID, group.ID) }).All(r.Context())
		if err != nil {
			return view, err
		}
		for _, link := range links {
			checked[link.Permission.ID] = true
		}
	}
	groups, count, err := permissionGroups(r, checked, submitted, "permissions")
	view.Groups, view.Count = groups, count
	return view, err
}

func (groupPermissionsExtra) validate(*Request, any, bool, url.Values) string { return "" }

func (groupPermissionsExtra) save(r *Request, object any, form url.Values) error {
	if !r.User.IsSuperuser {
		return nil
	}
	group := object.(*auth.Group)
	ctx := r.Context()
	if _, err := query.Objects[auth.GroupPermission]().Where(func(m *auth.GroupPermission) query.Cond { return query.Eq(&m.Group.ID, group.ID) }).Delete(ctx); err != nil {
		return err
	}
	for _, id := range selectedIDs(form, "permissions") {
		if err := query.Create(ctx, &auth.GroupPermission{Group: models.Ref[auth.Group](group.ID), Permission: models.Ref[auth.Permission](id)}); err != nil {
			return err
		}
	}
	return nil
}
