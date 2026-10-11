package blog

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/yoav-rogat/djan-go/query"
)

type publishedPost struct {
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	Slug        string     `json:"slug"`
	Author      string     `json:"author"`
	Summary     *string    `json:"summary"`
	PublishedAt *time.Time `json:"published_at"`
}

// Routes registers the public JSON API.
func Routes(mux *http.ServeMux) {
	mux.Handle("GET /{$}", http.RedirectHandler("/api/posts", http.StatusFound))
	mux.HandleFunc("GET /api/posts", listPublishedPosts)
}

func listPublishedPosts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	published := query.Objects[Post]().
		Where(func(p *Post) query.Cond { return query.Eq(&p.Status, StatusPublished) }).
		OrderBy(func(p *Post) []query.Order { return []query.Order{query.Desc(&p.PublishedAt)} })
	posts, err := published.Preload(ctx, func(p *Post) []query.Relation { return []query.Relation{query.Related(&p.Author)} })
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response := make([]publishedPost, 0, len(posts))
	for _, post := range posts {
		item := publishedPost{ID: post.ID, Title: post.Title, Slug: post.Slug, Summary: post.Summary, PublishedAt: post.PublishedAt}
		if author := post.Author.Cached(); author != nil {
			item.Author = author.Name
		}
		response = append(response, item)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
