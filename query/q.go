package query

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Bokitza/djan-go/models"
)

// Q is the string-based fallback for dynamic filters, e.g. Q{"author__name__icontains": "yo"}.
// Keys are validated against the model; unknown fields or lookups produce an error at execution.
type Q map[string]any

func (Q) isCond() {}

var lookupNames = map[string]bool{
	"exact": true, "iexact": true, "ne": true, "contains": true, "icontains": true,
	"startswith": true, "istartswith": true, "endswith": true, "gt": true, "gte": true,
	"lt": true, "lte": true, "in": true, "isnull": true, "range": true,
}

// resolveQ turns a Q into resolved conditions rooted at scope.
func resolveQ(scope *models.ModelInfo, q Q) (Cond, error) {
	keys := make([]string, 0, len(q))
	for key := range q {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	conds := make([]Cond, 0, len(keys))
	for _, key := range keys {
		cond, err := resolvePath(scope, key, q[key])
		if err != nil {
			return nil, err
		}
		conds = append(conds, cond)
	}
	return And(conds...), nil
}

func resolvePath(scope *models.ModelInfo, key string, value any) (Cond, error) {
	parts := strings.Split(key, "__")
	operator := "exact"
	if len(parts) > 1 && lookupNames[parts[len(parts)-1]] {
		operator = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	field := scope.FieldByName(parts[0])
	if field == nil {
		return nil, fmt.Errorf("query: %s has no field %q (in %q)", scope.Name, parts[0], key)
	}
	if len(parts) > 1 {
		if field.Kind != models.KindForeignKey {
			return nil, fmt.Errorf("query: %s.%s is not a foreign key (in %q)", scope.Name, field.Name, key)
		}
		related := field.Related()
		if related == nil {
			return nil, fmt.Errorf("query: target of %s is not registered", field)
		}
		inner, err := resolvePath(related, strings.Join(parts[1:], "__")+"__"+operator, value)
		if err != nil {
			return nil, err
		}
		return FieldRel(field, inner), nil
	}
	values, err := lookupValues(operator, value)
	if err != nil {
		return nil, fmt.Errorf("query: %q: %w", key, err)
	}
	return FieldLookup(field, operator, values...), nil
}

func lookupValues(operator string, value any) ([]any, error) {
	switch operator {
	case "in", "range":
		reflected := reflect.ValueOf(value)
		if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
			return nil, fmt.Errorf("%s needs a slice, got %T", operator, value)
		}
		values := make([]any, reflected.Len())
		for index := range values {
			values[index] = reflected.Index(index).Interface()
		}
		if operator == "range" && len(values) != 2 {
			return nil, fmt.Errorf("range needs exactly two values")
		}
		return values, nil
	case "isnull":
		if _, ok := value.(bool); !ok {
			return nil, fmt.Errorf("isnull needs a bool, got %T", value)
		}
	}
	return []any{value}, nil
}
