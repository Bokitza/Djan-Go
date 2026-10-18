package admin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Bokitza/djan-go/models"
	"github.com/Bokitza/djan-go/query"
	"github.com/shopspring/decimal"
)

// cell is a rendered value in lists and read-only form fields.
type cell struct {
	Kind string // text, bool, null, datetime, link, choice, json, label, html
	Text string
	Bool bool
	URL  string
	Full string // untruncated text for tooltips
	HTML template.HTML
}

// richTextCell shows sanitized rich text; the value is sanitized again since rows may be written outside the admin.
func richTextCell(raw string) cell {
	return cell{Kind: "html", HTML: template.HTML(sanitizeRichText(raw))}
}

// isRichText reports whether the admin of the field's model edits it as rich text.
func (s *Site) isRichText(field *models.Field) bool {
	entry := s.entryFor(field.Model)
	return entry != nil && entry.config().richText[field]
}

func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

func formatUUID(value reflect.Value) string {
	var raw [16]byte
	reflect.Copy(reflect.ValueOf(&raw).Elem(), value)
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

// rawValue returns the field value without the nullable pointer, and whether it is NULL.
func rawValue(field *models.Field, object any) (reflect.Value, bool) {
	value := field.Addr(object)
	if field.Kind == models.KindForeignKey {
		if value.FieldByName("ID").Int() == 0 {
			return value, true
		}
		return value, false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return value, true
		}
		return value.Elem(), false
	}
	return value, false
}

func displayValue(site *Site, r *Request, field *models.Field, object any) cell {
	if object == nil {
		return cell{Kind: "null"}
	}
	value, isNull := rawValue(field, object)
	if isNull {
		return cell{Kind: "null"}
	}
	if len(field.Choices) > 0 {
		return cell{Kind: "choice", Text: field.ChoiceLabel(value.Interface())}
	}
	switch field.Kind {
	case models.KindBool:
		return cell{Kind: "bool", Bool: value.Bool()}
	case models.KindDateTime:
		moment := value.Interface().(time.Time)
		return cell{Kind: "datetime", Text: moment.Local().Format(site.dateTimeFormat()), Full: moment.Local().Format(time.RFC1123)}
	case models.KindDate:
		moment := value.Interface().(time.Time)
		return cell{Kind: "datetime", Text: moment.Format(site.dateFormat()), Full: moment.Format(time.DateOnly)}
	case models.KindDecimal:
		return cell{Kind: "number", Text: formatNumber(value.Interface().(decimal.Decimal), field.Places)}
	case models.KindJSON:
		encoded, _ := json.Marshal(value.Interface())
		return cell{Kind: "json", Text: truncate(string(encoded), 60), Full: string(encoded)}
	case models.KindBytes:
		return cell{Kind: "text", Text: fmt.Sprintf("%d bytes", value.Len())}
	case models.KindUUID:
		return cell{Kind: "text", Text: formatUUID(value)}
	case models.KindForeignKey:
		cached := value.Addr().MethodByName("Cached").Call(nil)[0]
		id := value.FieldByName("ID").Int()
		related := field.Related()
		label := fmt.Sprintf("#%d", id)
		if !cached.IsNil() {
			label = related.DisplayString(cached.Interface())
		}
		result := cell{Kind: "link", Text: truncate(label, 60), Full: label}
		if relatedEntry := site.entryFor(related); relatedEntry != nil && site.permissionsFor(r, relatedEntry).View {
			result.URL = site.objectURL(related, id)
		}
		return result
	}
	text := fmt.Sprint(value.Interface())
	if field.Kind.IsText() && site.isRichText(field) {
		text = richTextPlain(text)
	}
	return cell{Kind: "text", Text: truncate(text, 80), Full: text}
}

// loadLabels preloads the InLabel foreign keys of objects (and of what they reference) so their labels are complete.
func loadLabels(ctx context.Context, info *models.ModelInfo, objects []any) error {
	return loadLabelsUpTo(ctx, info, objects, 3)
}

func loadLabelsUpTo(ctx context.Context, info *models.ModelInfo, objects []any, depth int) error {
	if depth == 0 || len(objects) == 0 {
		return nil
	}
	for _, field := range info.Fields {
		if !field.InLabel {
			continue
		}
		if err := query.PreloadRelated(ctx, objects, field); err != nil {
			return err
		}
		if err := loadLabelsUpTo(ctx, field.Related(), loadedTargets(field, objects), depth-1); err != nil {
			return err
		}
	}
	return nil
}

// loadedTargets returns the already loaded objects that foreignKey points to from objects.
func loadedTargets(foreignKey *models.Field, objects []any) []any {
	var targets []any
	for _, object := range objects {
		if cached := foreignKey.Addr(object).Addr().MethodByName("Cached").Call(nil)[0]; !cached.IsNil() {
			targets = append(targets, cached.Interface())
		}
	}
	return targets
}

// followPath loads the foreign keys of path for objects and returns the final owners (nil when a link is NULL).
func followPath(ctx context.Context, objects []any, path []*models.Field) ([]any, error) {
	current := objects
	for _, foreignKey := range path {
		var present []any
		for _, object := range current {
			if object != nil {
				present = append(present, object)
			}
		}
		if err := query.PreloadRelated(ctx, present, foreignKey); err != nil {
			return nil, err
		}
		next := make([]any, len(current))
		for index, object := range current {
			if object == nil {
				continue
			}
			if targets := loadedTargets(foreignKey, []any{object}); len(targets) == 1 {
				next[index] = targets[0]
			}
		}
		current = next
	}
	return current, nil
}

// columnCells renders one column for every object.
func columnCells(ctx context.Context, site *Site, r *Request, info *models.ModelInfo, objects []any, column column) ([]cell, error) {
	owners, err := followPath(ctx, objects, column.path)
	if err != nil {
		return nil, err
	}
	cells := make([]cell, len(objects))
	if column.field == nil {
		if err := loadLabels(ctx, info, objects); err != nil {
			return nil, err
		}
		for index, object := range objects {
			label := info.DisplayString(object)
			cells[index] = cell{Kind: "label", Text: truncate(label, 80), Full: label}
		}
		return cells, nil
	}
	if column.field.Kind == models.KindForeignKey {
		var present []any
		for _, owner := range owners {
			if owner != nil {
				present = append(present, owner)
			}
		}
		if err := query.PreloadRelated(ctx, present, column.field); err != nil {
			return nil, err
		}
		if err := loadLabels(ctx, column.field.Related(), loadedTargets(column.field, present)); err != nil {
			return nil, err
		}
	}
	for index, owner := range owners {
		cells[index] = displayValue(site, r, column.field, owner)
	}
	return cells, nil
}

// formatNumber rounds to places, drops trailing zeros and groups thousands: 12345.50 -> "12,345.5".
func formatNumber(value decimal.Decimal, places int) string {
	text := value.Round(int32(places)).String()
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign, text = "-", text[1:]
	}
	whole, fraction, _ := strings.Cut(text, ".")
	var grouped strings.Builder
	for index, digit := range whole {
		if index > 0 && (len(whole)-index)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	if fraction != "" {
		return sign + grouped.String() + "." + fraction
	}
	return sign + grouped.String()
}

func humanList(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	}
	return strings.Join(values[:len(values)-1], ", ") + " and " + values[len(values)-1]
}
