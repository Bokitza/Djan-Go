package main

import "github.com/Bokitza/djan-go/models"

type Post struct {
	models.Model
	Title string
}

func main() {
	// want: (missing method Schema)
	models.Register[Post]()
}
