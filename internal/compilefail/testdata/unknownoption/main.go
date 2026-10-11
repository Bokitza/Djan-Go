package main

import "github.com/yoav/djan-go/models"

type Post struct {
	models.Model
	Body string
}

// want: has no field or method MaxLength
func (p *Post) Schema(s *models.Schema) { s.Text(&p.Body).MaxLength(10) }

func main() { models.Register[Post]() }
