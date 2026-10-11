package migrations

import (
	"fmt"
	"slices"
	"strings"
)

// Operation types.
const (
	CreateTable    = "create_table"
	DropTable      = "drop_table"
	AddColumn      = "add_column"
	DropColumn     = "drop_column"
	AlterColumn    = "alter_column"
	AddIndex       = "add_index"
	DropIndex      = "drop_index"
	AddForeignKey  = "add_foreign_key"
	DropForeignKey = "drop_foreign_key"
	RunSQL         = "run_sql"
)

// Operation is one schema change. Only the fields relevant to its Type are set.
type Operation struct {
	Type       string  `json:"type"`
	Table      string  `json:"table,omitempty"`
	TableState *Table  `json:"table_state,omitempty"`
	Column     *Column `json:"column,omitempty"`
	Previous   *Column `json:"previous,omitempty"`
	Index      *Index  `json:"index,omitempty"`
	SQL        string  `json:"sql,omitempty"`
	ReverseSQL string  `json:"reverse_sql,omitempty"`
}

// Describe is a one-line summary for terminal output.
func (o Operation) Describe() string {
	switch o.Type {
	case CreateTable:
		return "Create table " + o.TableState.Name
	case DropTable:
		return "Drop table " + o.TableState.Name
	case AddColumn:
		return fmt.Sprintf("Add column %s.%s", o.Table, o.Column.Name)
	case DropColumn:
		return fmt.Sprintf("Drop column %s.%s", o.Table, o.Previous.Name)
	case AlterColumn:
		return fmt.Sprintf("Alter column %s.%s", o.Table, o.Column.Name)
	case AddIndex:
		return fmt.Sprintf("Add index %s on %s(%s)", o.Index.Name, o.Table, strings.Join(o.Index.Columns, ", "))
	case DropIndex:
		return fmt.Sprintf("Drop index %s", o.Index.Name)
	case AddForeignKey:
		return fmt.Sprintf("Add foreign key %s.%s -> %s", o.Table, o.Column.Name, o.Column.ForeignKey.Table)
	case DropForeignKey:
		return fmt.Sprintf("Drop foreign key %s.%s", o.Table, o.Column.Name)
	case RunSQL:
		return "Run SQL"
	}
	return o.Type
}

func (o Operation) table(state *State) (*Table, error) {
	table, ok := state.Tables[o.Table]
	if !ok {
		return nil, fmt.Errorf("migrations: %s: table %q does not exist", o.Type, o.Table)
	}
	return table, nil
}

func copyColumn(column *Column) *Column {
	copied := *column
	if column.ForeignKey != nil {
		foreignKey := *column.ForeignKey
		copied.ForeignKey = &foreignKey
	}
	return &copied
}

// Apply mutates the state.
func (o Operation) Apply(state *State) error {
	switch o.Type {
	case CreateTable:
		if _, exists := state.Tables[o.TableState.Name]; exists {
			return fmt.Errorf("migrations: table %q already exists", o.TableState.Name)
		}
		table := &Table{Name: o.TableState.Name, Model: o.TableState.Model}
		for _, column := range o.TableState.Columns {
			copied := copyColumn(column)
			copied.ForeignKey = nil
			table.Columns = append(table.Columns, copied)
		}
		state.Tables[table.Name] = table
	case DropTable:
		delete(state.Tables, o.TableState.Name)
	case AddColumn:
		table, err := o.table(state)
		if err != nil {
			return err
		}
		copied := copyColumn(o.Column)
		copied.ForeignKey = nil
		table.Columns = append(table.Columns, copied)
	case DropColumn:
		table, err := o.table(state)
		if err != nil {
			return err
		}
		table.Columns = slices.DeleteFunc(table.Columns, func(column *Column) bool { return column.Name == o.Previous.Name })
		table.Indexes = slices.DeleteFunc(table.Indexes, func(index *Index) bool { return slices.Contains(index.Columns, o.Previous.Name) })
	case AlterColumn:
		table, err := o.table(state)
		if err != nil {
			return err
		}
		column := table.column(o.Column.Name)
		if column == nil {
			return fmt.Errorf("migrations: column %s.%s does not exist", o.Table, o.Column.Name)
		}
		foreignKey := column.ForeignKey
		*column = *copyColumn(o.Column)
		column.ForeignKey = foreignKey
	case AddIndex:
		table, err := o.table(state)
		if err != nil {
			return err
		}
		index := *o.Index
		table.Indexes = append(table.Indexes, &index)
	case DropIndex:
		table, err := o.table(state)
		if err != nil {
			return err
		}
		table.Indexes = slices.DeleteFunc(table.Indexes, func(index *Index) bool { return index.Name == o.Index.Name })
	case AddForeignKey, DropForeignKey:
		table, err := o.table(state)
		if err != nil {
			return err
		}
		column := table.column(o.Column.Name)
		if column == nil {
			return fmt.Errorf("migrations: column %s.%s does not exist", o.Table, o.Column.Name)
		}
		if o.Type == AddForeignKey {
			foreignKey := *o.Column.ForeignKey
			column.ForeignKey = &foreignKey
		} else {
			column.ForeignKey = nil
		}
	case RunSQL:
	default:
		return fmt.Errorf("migrations: unknown operation %q", o.Type)
	}
	return nil
}

func createTableSQL(table *Table) string {
	definitions := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		definitions[index] = "    " + columnDefinition(column)
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n)", quoteIdentifier(table.Name), strings.Join(definitions, ",\n"))
}

func createIndexSQL(table string, index *Index) string {
	columns := make([]string, len(index.Columns))
	for position, column := range index.Columns {
		columns[position] = quoteIdentifier(column)
	}
	unique := ""
	if index.Unique {
		unique = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", unique, quoteIdentifier(index.Name), quoteIdentifier(table), strings.Join(columns, ", "))
}

func addForeignKeySQL(table string, column *Column) string {
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s) ON DELETE %s",
		quoteIdentifier(table), quoteIdentifier(foreignKeyName(table, column.Name)), quoteIdentifier(column.Name),
		quoteIdentifier(column.ForeignKey.Table), quoteIdentifier(column.ForeignKey.Column), column.ForeignKey.OnDelete)
}

func dropForeignKeySQL(table string, column *Column) string {
	return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", quoteIdentifier(table), quoteIdentifier(foreignKeyName(table, column.Name)))
}

func addColumnSQL(table string, column *Column) []string {
	alter := "ALTER TABLE " + quoteIdentifier(table)
	if !column.Nullable && column.Default == "" && !column.PrimaryKey {
		if zero := zeroLiteral(column); zero != "" {
			return []string{
				alter + " ADD COLUMN " + columnDefinition(column) + " DEFAULT " + zero,
				alter + " ALTER COLUMN " + quoteIdentifier(column.Name) + " DROP DEFAULT",
			}
		}
	}
	return []string{alter + " ADD COLUMN " + columnDefinition(column)}
}

func alterColumnSQL(table string, from, to *Column) []string {
	alter := "ALTER TABLE " + quoteIdentifier(table) + " ALTER COLUMN " + quoteIdentifier(to.Name)
	var statements []string
	if from.Type != to.Type {
		statements = append(statements, fmt.Sprintf("%s TYPE %s USING %s::%s", alter, to.Type, quoteIdentifier(to.Name), to.Type))
	}
	if from.Default != to.Default {
		if to.Default == "" {
			statements = append(statements, alter+" DROP DEFAULT")
		} else {
			statements = append(statements, alter+" SET DEFAULT "+to.Default)
		}
	}
	if from.Nullable != to.Nullable {
		if to.Nullable {
			statements = append(statements, alter+" DROP NOT NULL")
		} else {
			statements = append(statements, alter+" SET NOT NULL")
		}
	}
	return statements
}

// Up returns the forward SQL.
func (o Operation) Up() []string {
	switch o.Type {
	case CreateTable:
		return []string{createTableSQL(o.TableState)}
	case DropTable:
		return []string{"DROP TABLE " + quoteIdentifier(o.TableState.Name)}
	case AddColumn:
		return addColumnSQL(o.Table, o.Column)
	case DropColumn:
		return []string{"ALTER TABLE " + quoteIdentifier(o.Table) + " DROP COLUMN " + quoteIdentifier(o.Previous.Name)}
	case AlterColumn:
		return alterColumnSQL(o.Table, o.Previous, o.Column)
	case AddIndex:
		return []string{createIndexSQL(o.Table, o.Index)}
	case DropIndex:
		return []string{"DROP INDEX " + quoteIdentifier(o.Index.Name)}
	case AddForeignKey:
		return []string{addForeignKeySQL(o.Table, o.Column)}
	case DropForeignKey:
		return []string{dropForeignKeySQL(o.Table, o.Column)}
	case RunSQL:
		return []string{o.SQL}
	}
	return nil
}

// Down returns the reverse SQL.
func (o Operation) Down() []string {
	switch o.Type {
	case CreateTable:
		return []string{"DROP TABLE " + quoteIdentifier(o.TableState.Name)}
	case DropTable:
		table := &Table{Name: o.TableState.Name}
		for _, column := range o.TableState.Columns {
			copied := copyColumn(column)
			copied.ForeignKey = nil
			table.Columns = append(table.Columns, copied)
		}
		statements := []string{createTableSQL(table)}
		for _, index := range o.TableState.Indexes {
			statements = append(statements, createIndexSQL(table.Name, index))
		}
		for _, column := range o.TableState.Columns {
			if column.ForeignKey != nil {
				statements = append(statements, addForeignKeySQL(table.Name, column))
			}
		}
		return statements
	case AddColumn:
		return []string{"ALTER TABLE " + quoteIdentifier(o.Table) + " DROP COLUMN " + quoteIdentifier(o.Column.Name)}
	case DropColumn:
		restored := copyColumn(o.Previous)
		restored.Nullable = true
		return []string{"ALTER TABLE " + quoteIdentifier(o.Table) + " ADD COLUMN " + columnDefinition(restored)}
	case AlterColumn:
		return alterColumnSQL(o.Table, o.Column, o.Previous)
	case AddIndex:
		return []string{"DROP INDEX " + quoteIdentifier(o.Index.Name)}
	case DropIndex:
		return []string{createIndexSQL(o.Table, o.Index)}
	case AddForeignKey:
		return []string{dropForeignKeySQL(o.Table, o.Column)}
	case DropForeignKey:
		return []string{addForeignKeySQL(o.Table, o.Column)}
	case RunSQL:
		if o.ReverseSQL == "" {
			return nil
		}
		return []string{o.ReverseSQL}
	}
	return nil
}
