package admin

import (
	"html"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

var (
	richTextPolicy  = bluemonday.UGCPolicy()
	plainTextPolicy = bluemonday.StrictPolicy()
	// blockBreaks keeps the words of adjacent blocks apart once the markup is removed.
	blockBreaks = strings.NewReplacer("</p>", " ", "<br>", " ", "</li>", " ", "</h1>", " ", "</h2>", " ", "</h3>", " ",
		"</h4>", " ", "</blockquote>", " ", "</pre>", " ")
)

// sanitizeRichText keeps the safe subset of submitted HTML. Markup without text or images, such as
// the empty paragraph of a cleared editor, becomes "" so required fields are still enforced.
func sanitizeRichText(raw string) string {
	sanitized := strings.TrimSpace(richTextPolicy.Sanitize(raw))
	if richTextPlain(sanitized) == "" && !strings.Contains(sanitized, "<img") {
		return ""
	}
	for strings.HasSuffix(sanitized, "<p></p>") {
		sanitized = strings.TrimSuffix(sanitized, "<p></p>")
	}
	return sanitized
}

// richTextPlain returns the text of HTML without its markup.
func richTextPlain(raw string) string {
	text := plainTextPolicy.Sanitize(blockBreaks.Replace(raw))
	return strings.Join(strings.Fields(html.UnescapeString(text)), " ")
}
