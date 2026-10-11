// Package blog is the example app: authors write posts, tag them and readers leave comments.
package blog

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/yoav-rogat/djan-go/auth"
	"github.com/yoav-rogat/djan-go/models"
)

// Post statuses.
const (
	StatusDraft     = "draft"
	StatusReview    = "review"
	StatusPublished = "published"
)

// Author is a writer profile, optionally linked to a login account.
type Author struct {
	models.Model
	Name  string
	Email string
	Bio   string
	User  models.FK[auth.User]
}

func (a *Author) Schema(s *models.Schema) {
	s.Ordering(models.Asc(&a.Name))
	s.Char(&a.Name, 120)
	s.Char(&a.Email, 254).Blank()
	s.Text(&a.Bio).Blank()
	models.ForeignKey(s, &a.User).Nullable().Unique().Verbose("Login account").Help("Lets this staff user write posts as the author.")
}

func (a *Author) String() string { return a.Name }

// Tag labels posts.
type Tag struct {
	models.Model
	Name string
	Slug string
}

func (t *Tag) Schema(s *models.Schema) {
	s.Ordering(models.Asc(&t.Name))
	s.Char(&t.Name, 50).Unique()
	s.Char(&t.Slug, 50).Unique()
}

func (t *Tag) String() string { return t.Name }

func (t *Tag) BeforeSave(ctx context.Context) error {
	if t.Slug == "" {
		t.Slug = slugify(t.Name)
	}
	return nil
}

// Post is a blog article.
type Post struct {
	models.Model
	Title       string
	Slug        string
	Author      models.FK[Author]
	Summary     *string
	Body        string
	Status      string
	Featured    bool
	Views       int
	PublishedAt *time.Time
}

func (p *Post) Schema(s *models.Schema) {
	s.Ordering(models.Desc(&p.CreatedAt))
	s.Char(&p.Title, 200)
	s.Char(&p.Slug, 200).Unique().Blank().Help("Leave empty to generate it from the title.")
	models.ForeignKey(s, &p.Author).OnDelete(models.Protect)
	s.NullableText(&p.Summary)
	s.Text(&p.Body)
	s.Char(&p.Status, 20).Choices(StatusDraft, StatusReview, StatusPublished).Default(StatusDraft).Index()
	s.Bool(&p.Featured).Default(false)
	s.Int(&p.Views).Default(0).ReadOnly()
	s.NullableDateTime(&p.PublishedAt).ReadOnly()
}

func (p *Post) String() string { return p.Title }

func (p *Post) Validate() error {
	if strings.TrimSpace(p.Body) == "" && p.Status == StatusPublished {
		return errors.New("a published post needs a body")
	}
	return nil
}

func (p *Post) BeforeSave(ctx context.Context) error {
	if p.Slug == "" {
		p.Slug = slugify(p.Title)
	}
	if p.Status == StatusPublished && p.PublishedAt == nil {
		now := time.Now()
		p.PublishedAt = &now
	}
	return nil
}

// PostTag links posts and tags.
type PostTag struct {
	models.Model
	Post models.FK[Post]
	Tag  models.FK[Tag]
}

func (m *PostTag) Schema(s *models.Schema) {
	s.Verbose("Post tag", "Post tags")
	models.ForeignKey(s, &m.Post)
	models.ForeignKey(s, &m.Tag)
	s.UniqueTogether(&m.Post, &m.Tag)
}

// Comment is a reader comment awaiting moderation.
type Comment struct {
	models.Model
	Post     models.FK[Post]
	Name     string
	Body     string
	Approved bool
}

func (c *Comment) Schema(s *models.Schema) {
	s.Ordering(models.Desc(&c.CreatedAt))
	models.ForeignKey(s, &c.Post)
	s.Char(&c.Name, 100).Verbose("Commenter")
	s.Text(&c.Body)
	s.Bool(&c.Approved).Default(false)
}

func (c *Comment) String() string {
	body := c.Body
	if len(body) > 40 {
		body = body[:40] + "…"
	}
	return c.Name + ": " + body
}

func slugify(text string) string {
	var builder strings.Builder
	dash := false
	for _, character := range strings.ToLower(text) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
			dash = false
		case !dash && builder.Len() > 0:
			builder.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(builder.String(), "-")
}

func init() {
	models.Register[Author]()
	models.Register[Tag]()
	models.Register[Post]()
	models.Register[PostTag]()
	models.Register[Comment]()
}
