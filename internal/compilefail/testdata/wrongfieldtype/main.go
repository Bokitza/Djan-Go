package main

import "github.com/Bokitza/djan-go/models"

type Post struct {
	models.Model
	Score int
}

// want: cannot use &p.Score (value of type *int) as *string value
func (p *Post) Schema(s *models.Schema) { s.Char(&p.Score, 10) }

func main() { models.Register[Post]() }
