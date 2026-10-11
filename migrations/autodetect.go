package migrations

import (
	"sort"

	"github.com/yoav-rogat/djan-go/models"
)

// TargetState builds the state declared by the given models.
func TargetState(infos []*models.ModelInfo) *State {
	state := NewState()
	for _, info := range infos {
		state.Tables[info.Table] = TableFromModel(info)
	}
	return state
}

func sortedTableNames(tables map[string]*Table) []string {
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Diff returns the operations that turn from into to. Operations are ordered so that
// dependencies are satisfied: drops first, then creations, then indexes and foreign keys.
func Diff(from, to *State) []Operation {
	var dropIndexes, dropForeignKeys, dropColumns, dropTables, createTables, addColumns, alterColumns, addIndexes, addForeignKeys []Operation

	for _, name := range sortedTableNames(to.Tables) {
		target := to.Tables[name]
		current, exists := from.Tables[name]
		if !exists {
			stripped := &Table{Name: target.Name, Model: target.Model}
			for _, column := range target.Columns {
				copied := copyColumn(column)
				copied.ForeignKey = nil
				stripped.Columns = append(stripped.Columns, copied)
			}
			createTables = append(createTables, Operation{Type: CreateTable, TableState: stripped})
			for _, index := range target.Indexes {
				addIndexes = append(addIndexes, Operation{Type: AddIndex, Table: name, Index: index})
			}
			for _, column := range target.Columns {
				if column.ForeignKey != nil {
					addForeignKeys = append(addForeignKeys, Operation{Type: AddForeignKey, Table: name, Column: copyColumn(column)})
				}
			}
			continue
		}

		for _, column := range current.Columns {
			if target.column(column.Name) == nil {
				dropColumns = append(dropColumns, Operation{Type: DropColumn, Table: name, Previous: copyColumn(column)})
			}
		}
		for _, column := range target.Columns {
			existing := current.column(column.Name)
			if existing == nil {
				added := copyColumn(column)
				added.ForeignKey = nil
				addColumns = append(addColumns, Operation{Type: AddColumn, Table: name, Column: added})
				if column.ForeignKey != nil {
					addForeignKeys = append(addForeignKeys, Operation{Type: AddForeignKey, Table: name, Column: copyColumn(column)})
				}
				continue
			}
			if !existing.sameDefinition(column) {
				next, previous := copyColumn(column), copyColumn(existing)
				next.ForeignKey, previous.ForeignKey = nil, nil
				alterColumns = append(alterColumns, Operation{Type: AlterColumn, Table: name, Column: next, Previous: previous})
			}
			if !existing.ForeignKey.equal(column.ForeignKey) {
				if existing.ForeignKey != nil {
					dropForeignKeys = append(dropForeignKeys, Operation{Type: DropForeignKey, Table: name, Column: copyColumn(existing)})
				}
				if column.ForeignKey != nil {
					addForeignKeys = append(addForeignKeys, Operation{Type: AddForeignKey, Table: name, Column: copyColumn(column)})
				}
			}
		}
		for _, index := range current.Indexes {
			targetIndex := target.index(index.Name)
			if targetIndex == nil || !targetIndex.equal(index) {
				dropIndexes = append(dropIndexes, Operation{Type: DropIndex, Table: name, Index: index})
			}
		}
		for _, index := range target.Indexes {
			currentIndex := current.index(index.Name)
			if currentIndex == nil || !currentIndex.equal(index) {
				addIndexes = append(addIndexes, Operation{Type: AddIndex, Table: name, Index: index})
			}
		}
	}

	for _, name := range sortedTableNames(from.Tables) {
		if _, kept := to.Tables[name]; kept {
			continue
		}
		table := from.Tables[name]
		for _, column := range table.Columns {
			if column.ForeignKey != nil {
				dropForeignKeys = append(dropForeignKeys, Operation{Type: DropForeignKey, Table: name, Column: copyColumn(column)})
			}
		}
		dropped := &Table{Name: table.Name, Model: table.Model, Indexes: table.Indexes}
		for _, column := range table.Columns {
			copied := copyColumn(column)
			copied.ForeignKey = nil
			dropped.Columns = append(dropped.Columns, copied)
		}
		dropTables = append(dropTables, Operation{Type: DropTable, TableState: dropped})
	}

	var operations []Operation
	for _, group := range [][]Operation{dropIndexes, dropForeignKeys, dropColumns, dropTables, createTables, addColumns, alterColumns, addIndexes, addForeignKeys} {
		operations = append(operations, group...)
	}
	return operations
}
