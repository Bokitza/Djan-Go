package blog

import (
	"context"
	"errors"

	"github.com/yoav-rogat/djan-go/admin"
	"github.com/yoav-rogat/djan-go/auth"
	"github.com/yoav-rogat/djan-go/importexport"
	"github.com/yoav-rogat/djan-go/query"
)

// AuthorFor returns the author profile linked to user, or nil.
func AuthorFor(ctx context.Context, user *auth.User) *Author {
	author, err := query.Objects[Author]().Get(ctx, func(a *Author) query.Cond { return query.Eq(&a.User.ID, user.ID) })
	if err != nil {
		return nil
	}
	return author
}

// writtenBy matches posts whose author is linked to user.
func writtenBy(p *Post, user *auth.User) query.Cond {
	return query.Rel(&p.Author, func(a *Author) query.Cond { return query.Eq(&a.User.ID, user.ID) })
}

// RegisterAdmin adds the blog models to site.
//
// Rules for staff users who are not superusers (editors):
//   - they see published posts and their own posts;
//   - they may only edit their own posts that are not published yet;
//   - they may only delete their own drafts;
//   - they may only add posts when they have an author profile, and can't publish.
func RegisterAdmin(site *admin.Site) {
	admin.Register(site, admin.ModelAdmin[Post]{
		Icon: "file-text",
		Configure: func(p *Post, config *admin.Config) {
			config.ListDisplay(
				admin.F(&p.Title),
				admin.Through(&p.Author, func(a *Author) admin.Field { return admin.F(&a.Name) }).Labeled("Author"),
				admin.F(&p.Status),
				admin.F(&p.Featured),
				admin.F(&p.Views),
				admin.F(&p.PublishedAt),
			)
			config.SearchFields(&p.Title, &p.Body)
			config.ListFilter(admin.F(&p.Status), admin.F(&p.Author), admin.F(&p.Featured), admin.F(&p.PublishedAt))
		},
		ViewQuerySet: func(r *admin.Request, posts query.QuerySet[Post]) query.QuerySet[Post] {
			if r.User.IsSuperuser {
				return posts
			}
			return posts.Where(func(p *Post) query.Cond {
				return query.Or(query.Eq(&p.Status, StatusPublished), writtenBy(p, r.User))
			})
		},
		ChangeQuerySet: func(r *admin.Request, posts query.QuerySet[Post]) query.QuerySet[Post] {
			if r.User.IsSuperuser {
				return posts
			}
			return posts.Where(func(p *Post) query.Cond {
				return query.And(writtenBy(p, r.User), query.Ne(&p.Status, StatusPublished))
			})
		},
		DeleteQuerySet: func(r *admin.Request, posts query.QuerySet[Post]) query.QuerySet[Post] {
			posts = posts.Where(func(p *Post) query.Cond { return query.Eq(&p.Status, StatusDraft) })
			if r.User.IsSuperuser {
				return posts
			}
			return posts.Where(func(p *Post) query.Cond { return writtenBy(p, r.User) })
		},
		CanAdd: func(r *admin.Request) bool {
			return r.User.IsSuperuser || AuthorFor(r.Context(), r.User) != nil
		},
		ImportExport: &admin.ImportExport[Post]{
			Import: true, Export: true, ExportSelected: true,
			Resources: []importexport.Resource[Post]{postResource},
		},
		BeforeSave: func(r *admin.Request, post *Post, isNew bool) error {
			if r.User.IsSuperuser {
				return nil
			}
			author := AuthorFor(r.Context(), r.User)
			if author == nil || post.Author.ID != author.ID {
				return errors.New("you can only write posts as yourself")
			}
			if post.Status == StatusPublished {
				return errors.New("only superusers can publish posts; submit it for review instead")
			}
			return nil
		},
	})

	admin.Register(site, admin.ModelAdmin[Author]{
		Icon: "user",
		Configure: func(a *Author, config *admin.Config) {
			config.ListDisplay(admin.F(&a.Name), admin.F(&a.Email), admin.Through(&a.User, func(u *auth.User) admin.Field { return admin.F(&u.Username) }).Labeled("Login"))
			config.SearchFields(&a.Name, &a.Email)
		},
		ChangeQuerySet: func(r *admin.Request, authors query.QuerySet[Author]) query.QuerySet[Author] {
			if r.User.IsSuperuser {
				return authors
			}
			return authors.Where(func(a *Author) query.Cond { return query.Eq(&a.User.ID, r.User.ID) })
		},
		ImportExport: &admin.ImportExport[Author]{
			Import: true, Export: true,
			Resources: []importexport.Resource[Author]{{
				Configure: func(a *Author, config *importexport.Config) {
					config.Fields(importexport.F(&a.Name), importexport.F(&a.Email), importexport.F(&a.Bio))
					config.ImportIDFields(importexport.F(&a.Name))
					config.SkipUnchanged()
				},
			}},
		},
	})

	admin.Register(site, admin.ModelAdmin[Tag]{
		Icon: "tag",
		Configure: func(t *Tag, config *admin.Config) {
			config.ListDisplay(admin.F(&t.Name), admin.F(&t.Slug), admin.F(&t.CreatedAt))
			config.SearchFields(&t.Name)
		},
		ImportExport: &admin.ImportExport[Tag]{Import: true, Export: true},
	})

	admin.Register(site, admin.ModelAdmin[PostTag]{
		Icon: "folder",
		Configure: func(m *PostTag, config *admin.Config) {
			config.ListDisplay(admin.F(&m.Post), admin.F(&m.Tag))
			config.ListFilter(admin.F(&m.Tag))
		},
	})

	admin.Register(site, admin.ModelAdmin[Comment]{
		Icon: "message-square",
		Configure: func(c *Comment, config *admin.Config) {
			config.ListDisplay(admin.F(&c.Name), admin.F(&c.Post), admin.F(&c.Approved), admin.F(&c.CreatedAt))
			config.SearchFields(&c.Name, &c.Body)
			config.ListFilter(admin.F(&c.Approved), admin.F(&c.Post), admin.F(&c.CreatedAt))
			config.PerPage(20)
		},
		ImportExport: &admin.ImportExport[Comment]{
			ExportSelected: true,
			Resources: []importexport.Resource[Comment]{{
				Configure: func(c *Comment, config *importexport.Config) {
					config.Fields(
						importexport.Through(&c.Post, func(p *Post) importexport.Field { return importexport.F(&p.Title) }).Header("post"),
						importexport.F(&c.Name),
						importexport.F(&c.Body),
						importexport.F(&c.Approved),
						importexport.F(&c.CreatedAt),
					)
				},
			}},
		},
	})
}

// postResource exchanges posts keyed by slug, with authors referenced by name.
var postResource = importexport.Resource[Post]{
	Name: "Posts",
	Configure: func(p *Post, config *importexport.Config) {
		config.Fields(
			importexport.F(&p.Slug),
			importexport.F(&p.Title),
			importexport.F(&p.Author).Widget(importexport.ForeignKey(func(a *Author) *string { return &a.Name })),
			importexport.F(&p.Status),
			importexport.F(&p.Featured),
			importexport.F(&p.Summary),
			importexport.F(&p.Body),
			importexport.F(&p.PublishedAt).ReadOnly(),
		)
		config.ImportIDFields(importexport.F(&p.Slug))
		config.SkipUnchanged()
	},
}
