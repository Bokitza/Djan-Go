package importexport

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/yoav/djan-go/models"
	"github.com/yoav/djan-go/query"
)

// Widget converts between a field value and the text of a dataset cell.
type Widget interface {
	// Clean parses a cell into a value accepted by models.Field.Set.
	Clean(ctx context.Context, cell string) (any, error)
	// Render formats the Go value of a field (as stored in the struct) as a cell.
	Render(ctx context.Context, value any) (string, error)
}

var errRequired = errors.New("This field is required.")

// fieldWidget is the default widget; it follows the field kind.
type fieldWidget struct{ field *models.Field }

func (w fieldWidget) Render(ctx context.Context, value any) (string, error) {
	if key, ok := value.(interface{ ForeignKeyID() int64 }); ok {
		if key.ForeignKeyID() == 0 {
			return "", nil
		}
		return strconv.FormatInt(key.ForeignKeyID(), 10), nil
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return "", nil
	}
	if reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return "", nil
		}
		reflected = reflected.Elem()
	}
	switch w.field.Kind {
	case models.KindDateTime:
		moment := reflected.Interface().(time.Time)
		if moment.IsZero() {
			return "", nil
		}
		return moment.Format(time.RFC3339), nil
	case models.KindJSON:
		encoded, err := json.Marshal(reflected.Interface())
		return string(encoded), err
	case models.KindBytes:
		return base64.StdEncoding.EncodeToString(reflected.Bytes()), nil
	case models.KindUUID:
		var raw [16]byte
		reflect.Copy(reflect.ValueOf(&raw).Elem(), reflected)
		encoded := hex.EncodeToString(raw[:])
		return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
	}
	return fmt.Sprint(reflected.Interface()), nil
}

func (w fieldWidget) Clean(ctx context.Context, cell string) (any, error) {
	field := w.field
	trimmed := strings.TrimSpace(cell)
	if field.Kind == models.KindChar || field.Kind == models.KindText {
		if cell == "" && field.Nullable {
			return nil, nil
		}
		if cell == "" && !field.Blank {
			return nil, errRequired
		}
		if field.MaxLength > 0 && utf8.RuneCountInString(cell) > field.MaxLength {
			return nil, fmt.Errorf("Ensure this value has at most %d characters (it has %d).", field.MaxLength, utf8.RuneCountInString(cell))
		}
		return w.checkChoice(cell)
	}
	if trimmed == "" {
		switch {
		case field.Kind == models.KindForeignKey:
			return int64(0), nil
		case field.Nullable:
			return nil, nil
		case field.Kind == models.KindBool:
			return false, nil
		case field.Blank && (field.Kind == models.KindJSON || field.Kind == models.KindBytes):
			return reflect.Zero(field.BaseType()).Interface(), nil
		}
		return nil, errRequired
	}
	switch field.Kind {
	case models.KindInt, models.KindInt64, models.KindForeignKey, models.KindAutoID:
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, errors.New("Enter a whole number.")
		}
		return w.checkChoice(parsed)
	case models.KindInt32:
		parsed, err := strconv.ParseInt(trimmed, 10, 32)
		if err != nil {
			return nil, errors.New("Enter a whole number between -2147483648 and 2147483647.")
		}
		return w.checkChoice(int32(parsed))
	case models.KindFloat:
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, errors.New("Enter a number.")
		}
		return w.checkChoice(parsed)
	case models.KindBool:
		switch strings.ToLower(trimmed) {
		case "true", "1", "yes", "y", "t":
			return true, nil
		case "false", "0", "no", "n", "f":
			return false, nil
		}
		return nil, errors.New("Enter true or false.")
	case models.KindDateTime:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
			if parsed, err := time.ParseInLocation(layout, trimmed, time.Local); err == nil {
				return parsed, nil
			}
		}
		return nil, errors.New("Enter a valid date and time.")
	case models.KindUUID:
		decoded, err := hex.DecodeString(strings.ReplaceAll(trimmed, "-", ""))
		if err != nil || len(decoded) != 16 {
			return nil, errors.New("Enter a valid UUID.")
		}
		var uuid [16]byte
		copy(uuid[:], decoded)
		return uuid, nil
	case models.KindJSON:
		target := reflect.New(field.BaseType())
		if err := json.Unmarshal([]byte(trimmed), target.Interface()); err != nil {
			return nil, fmt.Errorf("Enter valid JSON: %v", err)
		}
		return target.Elem().Interface(), nil
	case models.KindBytes:
		decoded, err := base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, errors.New("Enter base64 encoded data.")
		}
		return decoded, nil
	}
	return nil, fmt.Errorf("unsupported field kind %s", field.Kind)
}

func (w fieldWidget) checkChoice(value any) (any, error) {
	if len(w.field.Choices) == 0 {
		return value, nil
	}
	for _, choice := range w.field.Choices {
		if fmt.Sprint(choice) == fmt.Sprint(value) {
			return value, nil
		}
	}
	return nil, fmt.Errorf("Select a valid choice. %v is not one of the available choices.", value)
}

// foreignKeyWidget imports and exports a foreign key through a field of the related model.
type foreignKeyWidget[R any] struct {
	info  *models.ModelInfo
	field *models.Field
	err   error
}

// ForeignKey represents a foreign key by a field of the related model instead of its ID, like
// django-import-export's ForeignKeyWidget. The field should be unique:
//
//	importexport.F(&p.Author).Widget(importexport.ForeignKey(func(a *Author) *string { return &a.Name }))
func ForeignKey[R any, V any](build func(related *R) *V) Widget {
	probe, err := models.ProbeOf[R]()
	if err != nil {
		return &foreignKeyWidget[R]{err: err}
	}
	info := models.MustInfo[R]()
	field, ok := info.FieldAt(unsafe.Pointer(build(probe)), reflect.TypeFor[V]())
	if !ok {
		return &foreignKeyWidget[R]{err: fmt.Errorf("ForeignKey: pointer of type *%s is not a declared field of %s", reflect.TypeFor[V](), info.Name)}
	}
	return &foreignKeyWidget[R]{info: info, field: field}
}

func (w *foreignKeyWidget[R]) check() error { return w.err }

func (w *foreignKeyWidget[R]) loadsRelated() {}

func (w *foreignKeyWidget[R]) Clean(ctx context.Context, cell string) (any, error) {
	if w.err != nil {
		return nil, w.err
	}
	if strings.TrimSpace(cell) == "" {
		return int64(0), nil
	}
	value, err := fieldWidget{w.field}.Clean(ctx, cell)
	if err != nil {
		return nil, err
	}
	related, err := query.ForModel(w.info).Get(ctx, query.FieldLookup(w.field, "exact", w.field.ToDatabase(value)))
	switch {
	case errors.Is(err, query.ErrNotFound):
		return nil, fmt.Errorf("%s with %s %q does not exist.", w.info.VerboseName, strings.ToLower(w.field.VerboseName), cell)
	case errors.Is(err, query.ErrMultipleObjects):
		return nil, fmt.Errorf("More than one %s has %s %q.", strings.ToLower(w.info.VerboseName), strings.ToLower(w.field.VerboseName), cell)
	case err != nil:
		return nil, err
	}
	return w.info.PKOf(related), nil
}

func (w *foreignKeyWidget[R]) Render(ctx context.Context, value any) (string, error) {
	if w.err != nil {
		return "", w.err
	}
	key, ok := value.(models.FK[R])
	if !ok {
		return "", fmt.Errorf("ForeignKey widget for %s cannot render %T", w.info.Name, value)
	}
	if key.IsZero() {
		return "", nil
	}
	related, err := key.Get(ctx)
	if err != nil {
		return "", err
	}
	return fieldWidget{w.field}.Render(ctx, w.field.Addr(related).Interface())
}
