// Package migrations generates and applies Django-style schema migrations.
//
// makemigrations compares the schema stored in migration files (replayed into a State) with the
// schema declared by registered models, and writes a JSON file containing the operations together
// with the generated up and down SQL. migrate applies pending files inside transactions.
package migrations

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/yoav-rogat/djan-go/models"
)

// State is the database schema described by a sequence of migrations.
type State struct {
	Tables map[string]*Table `json:"tables"`
}

// Table is the state of one table.
type Table struct {
	Name    string    `json:"name"`
	Model   string    `json:"model,omitempty"`
	Columns []*Column `json:"columns"`
	Indexes []*Index  `json:"indexes,omitempty"`
}

// Column is the state of one column.
type Column struct {
	Name       string      `json:"name"`
	Type       string      `json:"type"`
	Nullable   bool        `json:"nullable,omitempty"`
	Default    string      `json:"default,omitempty"` // SQL expression
	PrimaryKey bool        `json:"primary_key,omitempty"`
	ForeignKey *ForeignKey `json:"foreign_key,omitempty"`
}

// ForeignKey is a column's reference to another table.
type ForeignKey struct {
	Table    string `json:"table"`
	Column   string `json:"column"`
	OnDelete string `json:"on_delete"`
}

// Index is a (possibly unique, possibly composite) index.
type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
}

// NewState returns an empty state.
func NewState() *State { return &State{Tables: map[string]*Table{}} }

func (s *State) clone() *State {
	encoded, _ := json.Marshal(s)
	copied := NewState()
	_ = json.Unmarshal(encoded, copied)
	if copied.Tables == nil {
		copied.Tables = map[string]*Table{}
	}
	return copied
}

func (t *Table) column(name string) *Column {
	for _, column := range t.Columns {
		if column.Name == name {
			return column
		}
	}
	return nil
}

func (t *Table) index(name string) *Index {
	for _, index := range t.Indexes {
		if index.Name == name {
			return index
		}
	}
	return nil
}

func (c *Column) sameDefinition(other *Column) bool {
	return c.Type == other.Type && c.Nullable == other.Nullable && c.Default == other.Default && c.PrimaryKey == other.PrimaryKey
}

func (f *ForeignKey) equal(other *ForeignKey) bool {
	if f == nil || other == nil {
		return f == other
	}
	return *f == *other
}

func (i *Index) equal(other *Index) bool {
	return i.Unique == other.Unique && slices.Equal(i.Columns, other.Columns)
}

// identifierName builds a Postgres identifier of at most 63 bytes.
func identifierName(table string, columns []string, suffix string) string {
	name := table + "_" + strings.Join(columns, "_") + "_" + suffix
	if len(name) <= 63 {
		return name
	}
	sum := sha1.Sum([]byte(name))
	hash := hex.EncodeToString(sum[:])[:8]
	return name[:63-len(suffix)-10] + "_" + hash + "_" + suffix
}

func foreignKeyName(table, column string) string {
	return identifierName(table, []string{column}, "fkey")
}

// TableFromModel derives the target table state from a registered model.
func TableFromModel(info *models.ModelInfo) *Table {
	table := &Table{Name: info.Table, Model: info.Label()}
	for _, field := range info.Fields {
		column := &Column{
			Name:       field.Column,
			Type:       field.DatabaseType(),
			Nullable:   field.Nullable,
			PrimaryKey: field.PrimaryKey,
		}
		if field.Auto && field.Kind == models.KindDateTime {
			column.Default = "now()"
		} else if field.HasDefault {
			column.Default = defaultLiteral(field.Default)
		}
		if field.Kind == models.KindForeignKey {
			if related := field.Related(); related != nil {
				column.ForeignKey = &ForeignKey{Table: related.Table, Column: related.PK.Column, OnDelete: string(field.OnDelete)}
			}
		}
		table.Columns = append(table.Columns, column)
		switch {
		case field.Unique:
			table.Indexes = append(table.Indexes, &Index{Name: identifierName(info.Table, []string{field.Column}, "key"), Columns: []string{field.Column}, Unique: true})
		case field.Indexed:
			table.Indexes = append(table.Indexes, &Index{Name: identifierName(info.Table, []string{field.Column}, "idx"), Columns: []string{field.Column}})
		}
	}
	addGroup := func(fields []*models.Field, unique bool) {
		columns := make([]string, len(fields))
		for index, field := range fields {
			columns[index] = field.Column
		}
		suffix := "idx"
		if unique {
			suffix = "uniq"
		}
		table.Indexes = append(table.Indexes, &Index{Name: identifierName(info.Table, columns, suffix), Columns: columns, Unique: unique})
	}
	for _, group := range info.UniqueTogether {
		addGroup(group, true)
	}
	for _, group := range info.Indexes {
		addGroup(group, false)
	}
	return table
}

func defaultLiteral(value any) string {
	switch typed := value.(type) {
	case string:
		return quoteLiteral(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case decimal.Decimal:
		return typed.String()
	case time.Time, []byte:
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return quoteLiteral(string(encoded)) + "::jsonb"
}

func quoteLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func quoteIdentifier(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func zeroLiteral(column *Column) string {
	switch {
	case strings.HasPrefix(column.Type, "varchar"), column.Type == "text":
		return "''"
	case column.Type == "bigint", column.Type == "integer", column.Type == "double precision", strings.HasPrefix(column.Type, "numeric"):
		if column.ForeignKey != nil {
			return ""
		}
		return "0"
	case column.Type == "boolean":
		return "false"
	case column.Type == "timestamptz", column.Type == "date":
		return "now()"
	case column.Type == "bytea":
		return "''::bytea"
	case column.Type == "jsonb":
		return "'null'::jsonb"
	case column.Type == "uuid":
		return "gen_random_uuid()"
	}
	return ""
}

func columnDefinition(column *Column) string {
	if column.PrimaryKey {
		return fmt.Sprintf("%s %s GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY", quoteIdentifier(column.Name), column.Type)
	}
	definition := quoteIdentifier(column.Name) + " " + column.Type
	if !column.Nullable {
		definition += " NOT NULL"
	}
	if column.Default != "" {
		definition += " DEFAULT " + column.Default
	}
	return definition
}
