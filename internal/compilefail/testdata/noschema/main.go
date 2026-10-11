package main

import "github.com/yoav-rogat/djan-go/models"

type Post struct {
	models.Model
	Title string
}

func main() {
	// want: (missing method Schema)
	models.Register[Post]()
}
