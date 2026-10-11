package main

import (
	"github.com/yoav/djan-go/models"
	"github.com/yoav/djan-go/query"
)

type Author struct {
	models.Model
	Name string
}

func (a *Author) Schema(s *models.Schema) { s.Char(&a.Name, 10) }

type Tag struct {
	models.Model
	Label string
}

func (t *Tag) Schema(s *models.Schema) { s.Char(&t.Label, 10) }

type Post struct {
	models.Model
	Author models.FK[Author]
}

func (p *Post) Schema(s *models.Schema) { models.ForeignKey(s, &p.Author) }

func main() {
	// want: type func(t *Tag) query.Cond of func(t *Tag) query.Cond {…} does not match
	query.Objects[Post]().Where(func(p *Post) query.Cond {
		return query.Rel(&p.Author, func(t *Tag) query.Cond { return query.Eq(&t.Label, "x") })
	})
}
