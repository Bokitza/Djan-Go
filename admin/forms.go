package admin

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yoav/djan-go/models"
	"github.com/yoav/djan-go/query"
)

type choiceOption struct {
	Value, Label string
	Selected     bool
}

// formField is a rendered form input.
type formField struct {
	Name, Label, Help, Widget, Value, Error string
	InputType                               string
	Required, ReadOnly, Checked, Nullable   bool
	MaxLength                               int
	Step                                    string
	Choices                                 []choiceOption
	AutocompleteURL, SelectedLabel          string
	Display                                 cell
	Wide                                    bool
}

var errRequired = errors.New("This field is required.")

func (s *Site) isEditable(entry modelEntry, field *models.Field) bool {
	config := entry.config()
	return !field.Auto && field.Editable && !config.readonly[field] && !config.exclude[field] && field.Kind != models.KindBytes
}

func (s *Site) isVisible(entry modelEntry, field *models.Field) bool {
	return !field.Auto && !entry.config().exclude[field]
}

func formatInput(field *models.Field, object any) string {
	value, isNull := rawValue(field, object)
	if isNull {
		return ""
	}
	switch field.Kind {
	case models.KindForeignKey:
		return strconv.FormatInt(value.FieldByName("ID").Int(), 10)
	case models.KindDateTime:
		moment := value.Interface().(time.Time)
		if moment.IsZero() {
			return ""
		}
		return moment.Local().Format("2006-01-02T15:04")
	case models.KindJSON:
		encoded, _ := json.MarshalIndent(value.Interface(), "", "  ")
		return string(encoded)
	case models.KindUUID:
		if value.IsZero() {
			return ""
		}
		return formatUUID(value)
	}
	return fmt.Sprint(value.Interface())
}

func (s *Site) buildFormFields(r *Request, entry modelEntry, object any, editable bool, submitted url.Values, fieldErrors map[string]string) []formField {
	info := entry.info()
	var fields []formField
	for _, field := range info.Fields {
		if !s.isVisible(entry, field) {
			continue
		}
		name := field.LookupName()
		rendered := formField{
			Name: name, Label: field.VerboseName, Help: field.HelpText, Error: fieldErrors[name],
			Nullable: field.Nullable, MaxLength: field.MaxLength,
			Required: !field.Blank && !field.Nullable && field.Kind != models.KindBool,
		}
		if !editable || !s.isEditable(entry, field) {
			rendered.Widget, rendered.ReadOnly = "readonly", true
			if field.Kind == models.KindForeignKey {
				_ = query.PreloadRelated(r.Context(), []any{object}, field)
			}
			rendered.Display = displayValue(s, r, field, object)
			fields = append(fields, rendered)
			continue
		}
		rendered.Value = formatInput(field, object)
		if submitted != nil {
			rendered.Value = submitted.Get(name)
		}
		switch {
		case len(field.Choices) > 0:
			rendered.Widget = "select"
			if field.Nullable || field.Blank {
				rendered.Choices = append(rendered.Choices, choiceOption{Value: "", Label: "---------", Selected: rendered.Value == ""})
			}
			for _, choice := range field.Choices {
				value := fmt.Sprint(choice)
				rendered.Choices = append(rendered.Choices, choiceOption{Value: value, Label: models.ChoiceLabel(choice), Selected: value == rendered.Value})
			}
		case field.Kind == models.KindChar:
			rendered.Widget, rendered.InputType = "input", "text"
			if strings.Contains(field.Column, "email") {
				rendered.InputType = "email"
			}
			if strings.Contains(field.Column, "url") || strings.Contains(field.Column, "website") {
				rendered.InputType = "url"
			}
		case field.Kind == models.KindText:
			rendered.Widget, rendered.Wide = "textarea", true
		case field.Kind == models.KindInt || field.Kind == models.KindInt32 || field.Kind == models.KindInt64:
			rendered.Widget, rendered.InputType, rendered.Step = "input", "number", "1"
		case field.Kind == models.KindFloat:
			rendered.Widget, rendered.InputType, rendered.Step = "input", "number", "any"
		case field.Kind == models.KindBool && field.Nullable:
			rendered.Widget = "select"
			rendered.Choices = []choiceOption{
				{Value: "", Label: "Unknown", Selected: rendered.Value == ""},
				{Value: "true", Label: "Yes", Selected: rendered.Value == "true"},
				{Value: "false", Label: "No", Selected: rendered.Value == "false"},
			}
		case field.Kind == models.KindBool:
			rendered.Widget = "checkbox"
			rendered.Checked = rendered.Value == "true"
			if submitted != nil {
				rendered.Checked = submitted.Has(name)
			}
		case field.Kind == models.KindDateTime:
			rendered.Widget, rendered.InputType = "input", "datetime-local"
		case field.Kind == models.KindJSON:
			rendered.Widget, rendered.Wide = "json", true
		case field.Kind == models.KindForeignKey:
			related := field.Related()
			relatedEntry := s.entryFor(related)
			if relatedEntry != nil && s.permissionsFor(r, relatedEntry).View {
				rendered.Widget = "fk"
				rendered.AutocompleteURL = s.modelURL(related) + "autocomplete/"
				if id, err := strconv.ParseInt(rendered.Value, 10, 64); err == nil && id > 0 {
					if selected, err := relatedEntry.viewQuerySet(r).GetByPK(r.Context(), id); err == nil {
						rendered.SelectedLabel = related.DisplayString(selected)
					} else {
						rendered.SelectedLabel = fmt.Sprintf("#%d", id)
					}
				}
			} else {
				rendered.Widget, rendered.InputType, rendered.Step = "input", "number", "1"
				rendered.Help = strings.TrimSpace(rendered.Help + " Enter the " + related.VerboseName + " ID.")
			}
		default:
			rendered.Widget, rendered.InputType = "input", "text"
		}
		fields = append(fields, rendered)
	}
	return fields
}

// parseInput converts a submitted string into a value for field.Set.
func parseInput(field *models.Field, raw string, present bool) (any, error) {
	if field.Kind == models.KindBool {
		if field.Nullable {
			switch raw {
			case "":
				return nil, nil
			case "true":
				return true, nil
			case "false":
				return false, nil
			}
			return nil, errors.New("Select a valid choice.")
		}
		return present && raw != "" && raw != "false" && raw != "0", nil
	}
	empty := strings.TrimSpace(raw) == ""
	if empty && field.Kind != models.KindChar && field.Kind != models.KindText {
		if field.Kind == models.KindForeignKey {
			if field.Nullable {
				return int64(0), nil
			}
			return nil, errRequired
		}
		if field.Nullable {
			return nil, nil
		}
		if field.Blank && field.Kind == models.KindJSON {
			return reflect.Zero(field.GoType).Interface(), nil
		}
		return nil, errRequired
	}
	var value any
	switch field.Kind {
	case models.KindChar, models.KindText:
		if raw == "" {
			if field.Nullable {
				return nil, nil
			}
			if !field.Blank {
				return nil, errRequired
			}
		}
		if field.MaxLength > 0 && utf8.RuneCountInString(raw) > field.MaxLength {
			return nil, fmt.Errorf("Ensure this value has at most %d characters (it has %d).", field.MaxLength, utf8.RuneCountInString(raw))
		}
		value = raw
	case models.KindInt, models.KindInt64, models.KindForeignKey:
		parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, errors.New("Enter a whole number.")
		}
		value = parsed
	case models.KindInt32:
		parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
		if err != nil {
			return nil, errors.New("Enter a whole number between -2147483648 and 2147483647.")
		}
		value = int32(parsed)
	case models.KindFloat:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, errors.New("Enter a number.")
		}
		value = parsed
	case models.KindDateTime:
		var parsed time.Time
		var err error
		for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05", time.RFC3339} {
			if parsed, err = time.ParseInLocation(layout, strings.TrimSpace(raw), time.Local); err == nil {
				break
			}
		}
		if err != nil {
			return nil, errors.New("Enter a valid date and time.")
		}
		value = parsed
	case models.KindUUID:
		decoded, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(raw), "-", ""))
		if err != nil || len(decoded) != 16 {
			return nil, errors.New("Enter a valid UUID.")
		}
		var uuid [16]byte
		copy(uuid[:], decoded)
		value = uuid
	case models.KindJSON:
		target := reflect.New(field.GoType)
		if err := json.Unmarshal([]byte(raw), target.Interface()); err != nil {
			return nil, fmt.Errorf("Enter valid JSON: %v", err)
		}
		return target.Elem().Interface(), nil
	default:
		return nil, fmt.Errorf("unsupported field %s", field.Kind)
	}
	if len(field.Choices) > 0 {
		valid := false
		for _, choice := range field.Choices {
			valid = valid || fmt.Sprint(choice) == fmt.Sprint(value)
		}
		if !valid {
			return nil, errors.New("Select a valid choice.")
		}
	}
	return value, nil
}

// bindForm copies submitted values into object and returns per-field errors.
func (s *Site) bindForm(r *Request, entry modelEntry, object any, form url.Values) map[string]string {
	fieldErrors := map[string]string{}
	for _, field := range entry.info().Fields {
		if !s.isEditable(entry, field) {
			continue
		}
		name := field.LookupName()
		value, err := parseInput(field, form.Get(name), form.Has(name))
		if err != nil {
			fieldErrors[name] = err.Error()
			continue
		}
		if field.Kind == models.KindForeignKey {
			unchanged := field.Get(object) == value
			if id, _ := value.(int64); id != 0 && !unchanged && !s.relatedVisible(r, field, id) {
				fieldErrors[name] = "Select a valid choice. That choice is not one of the available choices."
				continue
			}
		}
		if err := field.Set(object, value); err != nil {
			fieldErrors[name] = err.Error()
		}
	}
	return fieldErrors
}

func (s *Site) relatedVisible(r *Request, field *models.Field, id int64) bool {
	related := field.Related()
	queryset := query.ForModel(related)
	if relatedEntry := s.entryFor(related); relatedEntry != nil {
		if !s.permissionsFor(r, relatedEntry).View {
			return r.User.IsSuperuser
		}
		queryset = relatedEntry.viewQuerySet(r)
	}
	_, err := queryset.GetByPK(r.Context(), id)
	return err == nil
}
