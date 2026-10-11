package main

import (
	"github.com/yoav/djan-go/models"
	"github.com/yoav/djan-go/query"
)

type Post struct {
	models.Model
	Title string
}

func (p *Post) Schema(s *models.Schema) { s.Char(&p.Title, 10) }

func main() {
	// want: type *string of &p.Title does not match **V
	query.Objects[Post]().Where(func(p *Post) query.Cond { return query.IsNull(&p.Title, true) })
}
