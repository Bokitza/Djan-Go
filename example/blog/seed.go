package blog

import (
	"context"
	"fmt"
	"time"

	"github.com/yoav-rogat/djan-go/auth"
	"github.com/yoav-rogat/djan-go/db"
	"github.com/yoav-rogat/djan-go/models"
	"github.com/yoav-rogat/djan-go/query"
)

// EditorPermissions are granted to the "Editors" group created by Seed.
var EditorPermissions = []string{
	"blog.view_post", "blog.add_post", "blog.change_post", "blog.delete_post",
	"blog.view_author", "blog.change_author",
	"blog.view_tag", "blog.add_tag",
	"blog.view_posttag", "blog.add_posttag",
	"blog.view_comment", "blog.change_comment",
}

// Seed creates demo content and two editor accounts (ada / grace, password "editor-password").
// It does nothing when authors already exist.
func Seed(ctx context.Context, _ []string) error {
	if exists, err := query.Objects[Author]().Exists(ctx); err != nil || exists {
		if exists {
			fmt.Println("The database already has blog content; nothing to do.")
		}
		return err
	}
	return db.Atomic(ctx, func(ctx context.Context) error {
		editors := models.New[auth.Group]()
		editors.Name = "Editors"
		if err := query.Create(ctx, editors); err != nil {
			return err
		}
		for _, permission := range EditorPermissions {
			if err := auth.GrantGroupPermission(ctx, editors, permission); err != nil {
				return err
			}
		}

		authors := map[string]*Author{}
		for _, profile := range []struct{ username, name, email, bio string }{
			{"ada", "Ada Lovelace", "ada@example.com", "Writes about engines, analytical and otherwise."},
			{"grace", "Grace Hopper", "grace@example.com", "Compilers, debugging and nanoseconds."},
		} {
			user, err := auth.CreateUser(ctx, profile.username, profile.email, "editor-password")
			if err != nil {
				return err
			}
			user.IsStaff = true
			if err := query.Save(ctx, user); err != nil {
				return err
			}
			if err := auth.AddToGroup(ctx, user, editors); err != nil {
				return err
			}
			author := &Author{Name: profile.name, Email: profile.email, Bio: profile.bio, User: models.RefTo(user)}
			if err := query.Create(ctx, author); err != nil {
				return err
			}
			authors[profile.username] = author
		}
		guest := &Author{Name: "Guest Writer", Bio: "Occasional contributor without a login."}
		if err := query.Create(ctx, guest); err != nil {
			return err
		}

		tags := map[string]*Tag{}
		for _, name := range []string{"Go", "Postgres", "History", "Tutorial"} {
			tag := &Tag{Name: name}
			if err := query.Create(ctx, tag); err != nil {
				return err
			}
			tags[name] = tag
		}

		summary := "A short tour."
		posts := []struct {
			title, status string
			author        *Author
			featured      bool
			views         int
			tags          []string
		}{
			{"Notes on the Analytical Engine", StatusPublished, authors["ada"], true, 1843, []string{"History"}},
			{"Bernoulli numbers, step by step", StatusReview, authors["ada"], false, 0, []string{"History", "Tutorial"}},
			{"Poetical science", StatusDraft, authors["ada"], false, 0, nil},
			{"The first actual bug", StatusPublished, authors["grace"], true, 947, []string{"History"}},
			{"Why compilers matter", StatusDraft, authors["grace"], false, 0, []string{"Go"}},
			{"Visualizing a nanosecond", StatusPublished, authors["grace"], false, 300, []string{"Tutorial"}},
			{"Indexes in Postgres", StatusPublished, guest, false, 120, []string{"Postgres", "Tutorial"}},
			{"Generics in practice", StatusDraft, guest, false, 0, []string{"Go"}},
		}
		for index, item := range posts {
			post := models.New[Post]()
			post.Title, post.Status, post.Author, post.Featured, post.Views = item.title, item.status, models.RefTo(item.author), item.featured, item.views
			post.Body = "Lorem ipsum dolor sit amet, consectetur adipiscing elit.\n\nSed do eiusmod tempor incididunt."
			if index%2 == 0 {
				post.Summary = &summary
			}
			if item.status == StatusPublished {
				publishedAt := time.Now().AddDate(0, 0, -index*3)
				post.PublishedAt = &publishedAt
			}
			if err := query.Create(ctx, post); err != nil {
				return err
			}
			for _, name := range item.tags {
				if err := query.Create(ctx, &PostTag{Post: models.RefTo(post), Tag: models.RefTo(tags[name])}); err != nil {
					return err
				}
			}
			if item.status == StatusPublished {
				for number, name := range []string{"Charles", "Linus", "Barbara"} {
					comment := &Comment{Post: models.RefTo(post), Name: name, Body: "Great read, thanks for writing this!", Approved: number != 2}
					if err := query.Create(ctx, comment); err != nil {
						return err
					}
				}
			}
		}
		fmt.Println("Seeded blog content. Editors: ada / grace (password: editor-password).")
		return nil
	})
}
