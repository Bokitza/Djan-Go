package models

import (
	"strings"
	"unicode"
)

// SnakeCase converts a Go identifier to snake_case ("CreatedAt" -> "created_at", "UserID" -> "user_id").
func SnakeCase(name string) string {
	runes := []rune(name)
	var builder strings.Builder
	for index, character := range runes {
		if unicode.IsUpper(character) {
			if index > 0 {
				previousIsLower := unicode.IsLower(runes[index-1]) || unicode.IsDigit(runes[index-1])
				nextIsLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
				if previousIsLower || (nextIsLower && unicode.IsUpper(runes[index-1])) {
					builder.WriteByte('_')
				}
			}
			builder.WriteRune(unicode.ToLower(character))
		} else if character == '.' {
			builder.WriteByte('_')
		} else {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

// Humanize turns "created_at" or "CreatedAt" into "Created at".
func Humanize(name string) string {
	words := strings.Fields(strings.ReplaceAll(SnakeCase(name), "_", " "))
	if len(words) == 0 {
		return name
	}
	sentence := strings.Join(words, " ")
	if strings.HasSuffix(sentence, " id") && len(words) > 1 {
		sentence = strings.TrimSuffix(sentence, " id")
	}
	return strings.ToUpper(sentence[:1]) + sentence[1:]
}

// Pluralize is a small English pluralizer for verbose names.
func Pluralize(word string) string {
	lower := strings.ToLower(word)
	switch {
	case strings.HasSuffix(lower, "s"), strings.HasSuffix(lower, "x"), strings.HasSuffix(lower, "ch"), strings.HasSuffix(lower, "sh"):
		return word + "es"
	case strings.HasSuffix(lower, "y") && len(lower) > 1 && !strings.ContainsRune("aeiou", rune(lower[len(lower)-2])):
		return word[:len(word)-1] + "ies"
	}
	return word + "s"
}
