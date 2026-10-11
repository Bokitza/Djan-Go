package admin

import (
	"fmt"
	"maps"
	"strings"

	"github.com/yoav/djan-go/models"
)

// translations maps a language to English UI text and its translation. Texts without a
// translation are shown in English.
var translations = map[string]map[string]string{"he": hebrew}

var rightToLeft = map[string]bool{"he": true, "ar": true, "fa": true, "ur": true}

// Translate adds or overrides UI texts for the site's language, including model and field names
// of framework models (for example "Users" or "Username").
func (s *Site) Translate(messages map[string]string) {
	if s.messages == nil {
		s.messages = map[string]string{}
	}
	maps.Copy(s.messages, messages)
}

// AppLabel sets the label shown for an app in the navigation and dashboard.
func (s *Site) AppLabel(app, label string) {
	if s.appLabels == nil {
		s.appLabels = map[string]string{}
	}
	s.appLabels[app] = label
}

func (s *Site) direction() string {
	if rightToLeft[s.Language] {
		return "rtl"
	}
	return "ltr"
}

func (s *Site) translate(text string) string {
	if translated, ok := s.messages[text]; ok {
		return translated
	}
	if translated, ok := translations[s.Language][text]; ok {
		return translated
	}
	return text
}

func (s *Site) translatef(format string, arguments ...any) string {
	return fmt.Sprintf(s.translate(format), arguments...)
}

func (s *Site) appLabel(app string) string {
	if label, ok := s.appLabels[app]; ok {
		return label
	}
	return s.translate(titleCase(app))
}

func (s *Site) dateFormat() string {
	if s.DateFormat != "" {
		return s.DateFormat
	}
	if s.Language == "he" {
		return "02/01/2006"
	}
	return "Jan 2, 2006"
}

func (s *Site) dateTimeFormat() string {
	if s.DateTimeFormat != "" {
		return s.DateTimeFormat
	}
	if s.Language == "he" {
		return "02/01/2006 15:04"
	}
	return "Jan 2, 2006 15:04"
}

func (s *Site) languageCode() string {
	if s.Language == "" {
		return "en"
	}
	return s.Language
}

func (s *Site) modelName(info *models.ModelInfo) string { return s.translate(info.VerboseName) }

func (s *Site) modelNamePlural(info *models.ModelInfo) string {
	return s.translate(info.VerboseNamePlural)
}

func (s *Site) addTitle(info *models.ModelInfo) string {
	return s.translatef("Add %s", strings.ToLower(s.modelName(info)))
}
