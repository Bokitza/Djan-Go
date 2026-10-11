package main

import (
	"github.com/Bokitza/djan-go/models"
	"github.com/Bokitza/djan-go/query"
)

type Post struct {
	models.Model
	Views int
}

func (p *Post) Schema(s *models.Schema) { s.Int(&p.Views) }

func main() {
	// want: V (type int) does not satisfy ~string
	query.Objects[Post]().Where(func(p *Post) query.Cond { return query.IContains(&p.Views, 5) })
}
