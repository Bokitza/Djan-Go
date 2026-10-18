package query

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Bokitza/djan-go/db"
	"github.com/Bokitza/djan-go/models"
)

// QuoteIdentifier quotes a SQL identifier.
func QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

type compiler struct {
	dialect   db.Dialect
	arguments []any
	aliases   int
}

func newCompiler(ctx context.Context) *compiler {
	return &compiler{dialect: db.DialectFromContext(ctx)}
}

func (c *compiler) placeholder(value any) string {
	c.arguments = append(c.arguments, value)
	return c.dialect.Placeholder(len(c.arguments))
}

func (c *compiler) newAlias() string {
	c.aliases++
	return "t" + strconv.Itoa(c.aliases)
}

func column(alias string, field *models.Field) string {
	return QuoteIdentifier(alias) + "." + QuoteIdentifier(field.Column)
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func escapeGlob(value string) string {
	return strings.NewReplacer(`[`, `[[]`, `*`, `[*]`, `?`, `[?]`).Replace(value)
}

// textMatch renders contains/startswith/endswith lookups. SQLite's LIKE ignores ASCII case, so its
// case-sensitive lookups use GLOB; its case-insensitive ones use LIKE, which (like Postgres ILIKE
// with lower()) folds only ASCII letters.
func (c *compiler) textMatch(target, operator, text string) string {
	insensitive := strings.HasPrefix(operator, "i")
	if c.dialect.Name() == "sqlite" && !insensitive {
		pattern := escapeGlob(text)
		switch operator {
		case "contains":
			pattern = "*" + pattern + "*"
		case "startswith":
			pattern = pattern + "*"
		case "endswith":
			pattern = "*" + pattern
		}
		return target + " GLOB " + c.placeholder(pattern)
	}
	pattern := escapeLike(text)
	switch operator {
	case "contains", "icontains":
		pattern = "%" + pattern + "%"
	case "startswith", "istartswith":
		pattern = pattern + "%"
	case "endswith":
		pattern = "%" + pattern
	}
	if c.dialect.Name() == "sqlite" {
		return target + " LIKE " + c.placeholder(pattern) + ` ESCAPE '\'`
	}
	keyword := "LIKE"
	if insensitive {
		keyword = "ILIKE"
	}
	return target + " " + keyword + " " + c.placeholder(pattern)
}

var placeholderPattern = regexp.MustCompile(`\$(\d+)`)

func (c *compiler) compile(scope *models.ModelInfo, alias string, cond Cond) (string, error) {
	switch typed := cond.(type) {
	case nil:
		return "TRUE", nil
	case *errorCond:
		return "", typed.err
	case Q:
		resolved, err := resolveQ(scope, typed)
		if err != nil {
			return "", err
		}
		return c.compile(scope, alias, resolved)
	case *logicalCond:
		if len(typed.conds) == 0 {
			if typed.operator == "AND" {
				return "TRUE", nil
			}
			return "FALSE", nil
		}
		parts := make([]string, 0, len(typed.conds))
		for _, inner := range typed.conds {
			sql, err := c.compile(scope, alias, inner)
			if err != nil {
				return "", err
			}
			parts = append(parts, sql)
		}
		if len(parts) == 1 {
			return parts[0], nil
		}
		return "(" + strings.Join(parts, " "+typed.operator+" ") + ")", nil
	case *notCond:
		sql, err := c.compile(scope, alias, typed.inner)
		if err != nil {
			return "", err
		}
		return "NOT (" + sql + ")", nil
	case *rawCond:
		var replaceErr error
		sql := placeholderPattern.ReplaceAllStringFunc(typed.sql, func(match string) string {
			index, _ := strconv.Atoi(match[1:])
			if index < 1 || index > len(typed.arguments) {
				replaceErr = fmt.Errorf("query: Raw placeholder %s has no argument", match)
				return match
			}
			return c.placeholder(typed.arguments[index-1])
		})
		return "(" + sql + ")", replaceErr
	case *relationCond:
		if typed.err != nil {
			return "", typed.err
		}
		field := typed.field
		if field == nil {
			resolved, err := resolveIn(scope, typed.reference)
			if err != nil {
				return "", err
			}
			field = resolved
		}
		if field.Kind != models.KindForeignKey {
			return "", fmt.Errorf("query: Rel needs a foreign key, %s is a %s", field, field.Kind)
		}
		related := field.Related()
		if related == nil {
			return "", fmt.Errorf("query: target of %s is not registered", field)
		}
		subAlias := c.newAlias()
		inner, err := c.compile(related, subAlias, typed.inner)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s IN (SELECT %s FROM %s AS %s WHERE %s)",
			column(alias, field), column(subAlias, related.PK), QuoteIdentifier(related.Table), QuoteIdentifier(subAlias), inner), nil
	case *lookupCond:
		field := typed.field
		if field == nil {
			resolved, err := resolveIn(scope, typed.reference)
			if err != nil {
				return "", err
			}
			field = resolved
		}
		if field.Model != scope {
			return "", fmt.Errorf("query: field %s cannot be used in a %s query; reach it with query.Rel", field, scope.Name)
		}
		return c.lookup(alias, field, typed.operator, typed.values)
	}
	return "", fmt.Errorf("query: unsupported condition %T", cond)
}

func resolveIn(scope *models.ModelInfo, reference fieldReference) (*models.Field, error) {
	if field, ok := scope.FieldAt(reference.pointer, reference.typ); ok {
		return field, nil
	}
	field, err := models.ResolvePointer(reference.pointer, reference.typ)
	if err != nil {
		return nil, err
	}
	if field.Model != scope {
		return nil, fmt.Errorf("query: field %s cannot be used in a %s query; reach it with query.Rel", field, scope.Name)
	}
	return field, nil
}

func (c *compiler) lookup(alias string, field *models.Field, operator string, values []any) (string, error) {
	target := column(alias, field)
	databaseValues := make([]any, len(values))
	for index, value := range values {
		databaseValues[index] = field.ToDatabase(value)
	}
	single := func() any {
		if len(databaseValues) == 0 {
			return nil
		}
		return databaseValues[0]
	}
	textValue := func() (string, error) {
		if !field.Kind.IsText() {
			return "", fmt.Errorf("query: lookup %q needs a text field, %s is %s", operator, field, field.Kind)
		}
		return fmt.Sprint(single()), nil
	}
	switch operator {
	case "exact":
		if single() == nil {
			return target + " IS NULL", nil
		}
		return target + " = " + c.placeholder(single()), nil
	case "ne":
		if single() == nil {
			return target + " IS NOT NULL", nil
		}
		return target + " IS DISTINCT FROM " + c.placeholder(single()), nil
	case "in":
		if len(databaseValues) == 0 {
			return "FALSE", nil
		}
		placeholders := make([]string, len(databaseValues))
		for index, value := range databaseValues {
			placeholders[index] = c.placeholder(value)
		}
		return target + " IN (" + strings.Join(placeholders, ", ") + ")", nil
	case "gt", "gte", "lt", "lte":
		if field.Kind == models.KindBool || field.Kind == models.KindJSON {
			return "", fmt.Errorf("query: lookup %q is not supported on %s fields", operator, field.Kind)
		}
		symbol := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[operator]
		return target + " " + symbol + " " + c.placeholder(single()), nil
	case "range":
		if len(databaseValues) != 2 {
			return "", fmt.Errorf("query: range needs two values")
		}
		return target + " BETWEEN " + c.placeholder(databaseValues[0]) + " AND " + c.placeholder(databaseValues[1]), nil
	case "isnull":
		isNull, _ := values[0].(bool)
		if isNull {
			return target + " IS NULL", nil
		}
		return target + " IS NOT NULL", nil
	case "iexact":
		text, err := textValue()
		if err != nil {
			return "", err
		}
		return "lower(" + target + ") = lower(" + c.placeholder(text) + ")", nil
	case "contains", "icontains", "startswith", "istartswith", "endswith":
		text, err := textValue()
		if err != nil {
			return "", err
		}
		return c.textMatch(target, operator, text), nil
	}
	return "", fmt.Errorf("query: unknown lookup %q", operator)
}
