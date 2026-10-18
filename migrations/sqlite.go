package migrations

import (
	"fmt"
	"strings"

	"github.com/Bokitza/djan-go/db"
)

// sqliteUp renders operations for SQLite, starting from the state before them.
func sqliteUp(operations []Operation, before *State) ([]string, error) {
	state := before.clone()
	var statements []string
	for _, operation := range operations {
		rendered, err := sqliteOperation(operation, state)
		if err != nil {
			return nil, err
		}
		statements = append(statements, rendered...)
		if err := operation.Apply(state); err != nil {
			return nil, err
		}
	}
	return statements, nil
}

// sqliteDown renders the inverse of operations in reverse order, each against the state it undoes.
func sqliteDown(operations []Operation, before *State) ([]string, error) {
	after := make([]*State, len(operations))
	state := before.clone()
	for index, operation := range operations {
		if err := operation.Apply(state); err != nil {
			return nil, err
		}
		after[index] = state.clone()
	}
	var statements []string
	for index := len(operations) - 1; index >= 0; index-- {
		state := after[index]
		for _, inverse := range operations[index].inverse() {
			rendered, err := sqliteOperation(inverse, state)
			if err != nil {
				return nil, err
			}
			statements = append(statements, rendered...)
			if err := inverse.Apply(state); err != nil {
				return nil, err
			}
		}
	}
	return statements, nil
}

// inverse returns the operations that undo o, mirroring Down.
func (o Operation) inverse() []Operation {
	switch o.Type {
	case CreateTable:
		return []Operation{{Type: DropTable, TableState: o.TableState}}
	case DropTable:
		created := &Table{Name: o.TableState.Name, Model: o.TableState.Model}
		for _, column := range o.TableState.Columns {
			copied := copyColumn(column)
			copied.ForeignKey = nil
			created.Columns = append(created.Columns, copied)
		}
		operations := []Operation{{Type: CreateTable, TableState: created}}
		for _, index := range o.TableState.Indexes {
			operations = append(operations, Operation{Type: AddIndex, Table: created.Name, Index: index})
		}
		for _, column := range o.TableState.Columns {
			if column.ForeignKey != nil {
				operations = append(operations, Operation{Type: AddForeignKey, Table: created.Name, Column: copyColumn(column)})
			}
		}
		return operations
	case AddColumn:
		return []Operation{{Type: DropColumn, Table: o.Table, Previous: o.Column}}
	case DropColumn:
		restored := copyColumn(o.Previous)
		restored.Nullable = true
		restored.ForeignKey = nil
		return []Operation{{Type: AddColumn, Table: o.Table, Column: restored}}
	case AlterColumn:
		return []Operation{{Type: AlterColumn, Table: o.Table, Column: o.Previous, Previous: o.Column}}
	case AddIndex:
		return []Operation{{Type: DropIndex, Table: o.Table, Index: o.Index}}
	case DropIndex:
		return []Operation{{Type: AddIndex, Table: o.Table, Index: o.Index}}
	case AddForeignKey:
		return []Operation{{Type: DropForeignKey, Table: o.Table, Column: o.Column}}
	case DropForeignKey:
		return []Operation{{Type: AddForeignKey, Table: o.Table, Column: o.Column}}
	case RunSQL:
		return []Operation{{Type: RunSQL, SQL: o.ReverseSQL}}
	}
	return nil
}

// sqliteOperation renders one operation against the state before it. Changes SQLite's ALTER TABLE
// cannot express are done by rebuilding the table.
func sqliteOperation(o Operation, state *State) ([]string, error) {
	switch o.Type {
	case CreateTable:
		return []string{sqliteCreateTable(o.TableState.Name, o.TableState)}, nil
	case DropTable:
		return []string{"DROP TABLE " + quoteIdentifier(o.TableState.Name)}, nil
	case AddColumn:
		if o.Column.Nullable && o.Column.Default == "" && o.Column.ForeignKey == nil && !o.Column.PrimaryKey {
			return []string{"ALTER TABLE " + quoteIdentifier(o.Table) + " ADD COLUMN " + sqliteColumnDefinition(o.Column)}, nil
		}
		return sqliteRebuild(o, state)
	case DropColumn, AlterColumn, AddForeignKey, DropForeignKey:
		return sqliteRebuild(o, state)
	case AddIndex:
		return []string{createIndexSQL(o.Table, o.Index)}, nil
	case DropIndex:
		return []string{"DROP INDEX " + quoteIdentifier(o.Index.Name)}, nil
	case RunSQL:
		if o.SQL == "" {
			return nil, nil
		}
		return []string{o.SQL}, nil
	}
	return nil, fmt.Errorf("migrations: unknown operation %q", o.Type)
}

// sqliteRebuild copies the table into a new one with the changed definition, following
// https://www.sqlite.org/lang_altertable.html#otheralter. The runner disables foreign keys around it.
func sqliteRebuild(o Operation, before *State) ([]string, error) {
	current, err := o.table(before)
	if err != nil {
		return nil, err
	}
	after := before.clone()
	if err := o.Apply(after); err != nil {
		return nil, err
	}
	target := after.Tables[o.Table]
	temporary := "djan_rebuild_" + target.Name

	var columns, values []string
	for _, column := range target.Columns {
		switch {
		case current.column(column.Name) != nil:
			columns = append(columns, quoteIdentifier(column.Name))
			values = append(values, quoteIdentifier(column.Name))
		case !column.Nullable && column.Default == "" && !column.PrimaryKey:
			if zero := zeroLiteral(column); zero != "" {
				columns = append(columns, quoteIdentifier(column.Name))
				values = append(values, sqliteExpression(zero))
			}
		}
	}
	statements := []string{
		sqliteCreateTable(temporary, target),
		fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s", quoteIdentifier(temporary),
			strings.Join(columns, ", "), strings.Join(values, ", "), quoteIdentifier(target.Name)),
		"DROP TABLE " + quoteIdentifier(target.Name),
		fmt.Sprintf("ALTER TABLE %s RENAME TO %s", quoteIdentifier(temporary), quoteIdentifier(target.Name)),
	}
	for _, index := range target.Indexes {
		statements = append(statements, createIndexSQL(target.Name, index))
	}
	return statements, nil
}

func sqliteCreateTable(name string, table *Table) string {
	definitions := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		definitions[index] = "    " + sqliteColumnDefinition(column)
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n)", quoteIdentifier(name), strings.Join(definitions, ",\n"))
}

func sqliteColumnDefinition(column *Column) string {
	if column.PrimaryKey {
		return quoteIdentifier(column.Name) + " INTEGER PRIMARY KEY AUTOINCREMENT"
	}
	definition := quoteIdentifier(column.Name) + " " + db.SQLite.ColumnType(column.Type)
	if !column.Nullable {
		definition += " NOT NULL"
	}
	if column.Default != "" {
		definition += " DEFAULT " + sqliteExpression(column.Default)
	}
	if foreignKey := column.ForeignKey; foreignKey != nil {
		definition += fmt.Sprintf(" REFERENCES %s (%s) ON DELETE %s",
			quoteIdentifier(foreignKey.Table), quoteIdentifier(foreignKey.Column), foreignKey.OnDelete)
	}
	return definition
}

const sqliteRandomUUID = "(lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || " +
	"substr(lower(hex(randomblob(2))), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || " +
	"substr(lower(hex(randomblob(2))), 2) || '-' || lower(hex(randomblob(6))))"

// sqliteExpression translates a canonical (Postgres) default or zero expression.
func sqliteExpression(expression string) string {
	switch expression {
	case "now()":
		return "(" + db.SQLite.Now() + ")"
	case "gen_random_uuid()":
		return sqliteRandomUUID
	case "''::bytea":
		return "X''"
	}
	if literal, ok := strings.CutSuffix(expression, "::jsonb"); ok {
		return literal
	}
	return expression
}
