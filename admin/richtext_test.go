package admin

import (
	"testing"

	"github.com/Bokitza/djan-go/models"
)

func TestSanitizeRichText(t *testing.T) {
	for raw, want := range map[string]string{
		`<p onclick="steal()">Hi <a href="javascript:alert(1)">there</a></p><script>alert(1)</script>`: "<p>Hi there</p>",
		`<h2>Title</h2><ul><li>one</li></ul>`: `<h2>Title</h2><ul><li>one</li></ul>`,
		"<p></p>":                             "",
		"<p>Hi</p><p></p>":                    "<p>Hi</p>",
		"<p> <br></p>":                        "",
		`<p><img src="https://example.com/a.png"></p>`: `<p><img src="https://example.com/a.png"></p>`,
	} {
		if got := sanitizeRichText(raw); got != want {
			t.Errorf("sanitizeRichText(%q) = %q, want %q", raw, got, want)
		}
	}

	required := &models.Field{Kind: models.KindText}
	if _, err := parseInput(required, sanitizeRichText("<p></p>"), true); err == nil || err.Error() != errRequired.Error() {
		t.Fatalf("an empty editor on a required field: %v", err)
	}
}

func TestRichTextPlain(t *testing.T) {
	if got := richTextPlain("<h2>Title</h2><p>Fish &amp; <strong>chips</strong></p><ul><li>one</li><li>two</li></ul>"); got != "Title Fish & chips one two" {
		t.Fatalf("plain text: %q", got)
	}
}
