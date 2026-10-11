package admin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yoav/djan-go/models"
	"github.com/yoav/djan-go/query"
)

// cell is a rendered value in lists and read-only form fields.
type cell struct {
	Kind string // text, bool, null, datetime, link, choice, json, label
	Text string
	Bool bool
	URL  string
	Full string // untruncated text for tooltips
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
		return cell{Kind: "choice", Text: models.ChoiceLabel(value.Interface())}
	}
	switch field.Kind {
	case models.KindBool:
		return cell{Kind: "bool", Bool: value.Bool()}
	case models.KindDateTime:
		moment := value.Interface().(time.Time)
		return cell{Kind: "datetime", Text: moment.Local().Format("Jan 2, 2006 15:04"), Full: moment.Local().Format(time.RFC1123)}
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
	return cell{Kind: "text", Text: truncate(text, 80), Full: text}
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
			cached := foreignKey.Addr(object).Addr().MethodByName("Cached").Call(nil)[0]
			if !cached.IsNil() {
				next[index] = cached.Interface()
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
	}
	for index, owner := range owners {
		cells[index] = displayValue(site, r, column.field, owner)
	}
	return cells, nil
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
