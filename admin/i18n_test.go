package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestHebrewCoversEveryUIText(t *testing.T) {
	patterns := map[string]*regexp.Regexp{
		"templates/*.html": regexp.MustCompile(`\{\{tf? "([^"]+)"`),
		"*.go":             regexp.MustCompile(`(?:translatef?|invalid)\("([^"]+)"|format (?::?=) "([^"]+)"|(?:Label|Help): "([^"]+)"|view\.(?:Label|Help) = "([^"]+)"|return "([A-Z][^"]+\.)"`),
	}
	missing := map[string]bool{}
	for glob, pattern := range patterns {
		files, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") || file == "hebrew.go" {
				continue
			}
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
				for _, text := range match[1:] {
					if text != "" && hebrew[text] == "" {
						missing[file+": "+text] = true
					}
				}
			}
		}
	}
	for text := range missing {
		t.Errorf("no Hebrew translation for %q", text)
	}
}

func TestTranslationAndDirection(t *testing.T) {
	site := &Site{Language: "he"}
	if site.direction() != "rtl" || site.translate("Save") != "שמירה" {
		t.Fatalf("direction %q, Save %q", site.direction(), site.translate("Save"))
	}
	if got := site.translatef("Add %s", "תנועה"); got != "הוספת תנועה" {
		t.Fatalf("translatef = %q", got)
	}
	site.Translate(map[string]string{"Save": "לשמור"})
	site.AppLabel("budget", "תקציב")
	if site.translate("Save") != "לשמור" || site.appLabel("budget") != "תקציב" || site.appLabel("auth") != "הרשאות" {
		t.Fatal("overrides were not applied")
	}
	if english := (&Site{}); english.direction() != "ltr" || english.translate("Save") != "Save" || english.languageCode() != "en" {
		t.Fatal("English site should be left to right and untranslated")
	}
	if text := site.errorText(invalid("Ensure this value has at most %d characters (it has %d).", 5, 7)); text != "הערך יכול להכיל עד 5 תווים (יש בו 7)." {
		t.Fatalf("errorText = %q", text)
	}
}
