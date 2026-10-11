package main

import "github.com/yoav-rogat/djan-go/models"

type Post struct {
	models.Model
	Published bool
}

// want: cannot use "yes" (untyped string constant) as bool value
func (p *Post) Schema(s *models.Schema) { s.Bool(&p.Published).Default("yes") }

func main() { models.Register[Post]() }
