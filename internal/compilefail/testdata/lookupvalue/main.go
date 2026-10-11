package main

import (
	"github.com/yoav-rogat/djan-go/models"
	"github.com/yoav-rogat/djan-go/query"
)

type Post struct {
	models.Model
	Title string
}

func (p *Post) Schema(s *models.Schema) { s.Char(&p.Title, 10) }

func main() {
	// want: cannot use 5 (untyped int constant) as string value
	query.Objects[Post]().Where(func(p *Post) query.Cond { return query.Gte(&p.Title, 5) })
}
