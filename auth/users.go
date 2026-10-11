package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/yoav-rogat/djan-go/models"
	"github.com/yoav-rogat/djan-go/query"
)

var usernamePattern = regexp.MustCompile(`^[\w.@+-]+$`)

// Validate is called by the admin before saving.
func (u *User) Validate() error {
	if !usernamePattern.MatchString(u.Username) {
		return errors.New("username may only contain letters, digits and @/./+/-/_")
	}
	if u.Email != "" && !strings.Contains(u.Email, "@") {
		return errors.New("enter a valid email address")
	}
	return nil
}

// FullName returns "First Last".
func (u *User) FullName() string { return strings.TrimSpace(u.FirstName + " " + u.LastName) }

// CreateUser creates an active user with a hashed password.
func CreateUser(ctx context.Context, username, email, password string) (*User, error) {
	user := models.New[User]()
	user.Username, user.Email = username, email
	if err := user.Validate(); err != nil {
		return nil, err
	}
	if password == "" {
		user.SetUnusablePassword()
	} else if err := user.SetPassword(password); err != nil {
		return nil, err
	}
	return user, query.Create(ctx, user)
}

// CreateSuperuser creates an active staff superuser.
func CreateSuperuser(ctx context.Context, username, email, password string) (*User, error) {
	user := models.New[User]()
	user.Username, user.Email = username, email
	user.IsStaff, user.IsSuperuser = true, true
	if err := user.Validate(); err != nil {
		return nil, err
	}
	if err := user.SetPassword(password); err != nil {
		return nil, err
	}
	return user, query.Create(ctx, user)
}
