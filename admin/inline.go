package admin

import (
	"fmt"
	"log"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unsafe"

	"github.com/Bokitza/djan-go/models"
	"github.com/Bokitza/djan-go/query"
)

// Inline is a section of T's form that edits related rows. InlineAdmin implements it.
type Inline[T any] interface {
	editorFor(parent *T) (extraField, error)
}

// InlineAdmin edits the rows of model C that point at T through a foreign key, on the form of T
// (Django's TabularInline and StackedInline):
//
//	Inlines: []admin.Inline[Car]{
//		admin.InlineAdmin[Car, CarBodyToCarDiary]{Stacked: true},
//		admin.InlineAdmin[Car, Transaction]{
//			ForeignKey: func(t *Transaction) *models.FK[Car] { return &t.Car },
//			Configure: func(t *Transaction, c *admin.InlineConfig) {
//				c.Fields(admin.F(&t.Date), admin.F(&t.Amount), admin.F(&t.Description))
//			},
//		},
//	}
//
// Rows follow the rules of C's admin when C is registered: only rows its ViewQuerySet returns are
// listed, only rows its ChangeQuerySet and DeleteQuerySet return may be edited and deleted, its
// Readonly and ReadonlyWhen fields stay read-only and its BeforeSave and AfterSave hooks run.
// Without an admin for C the user's model permissions on C apply.
type InlineAdmin[T, C any] struct {
	// ForeignKey picks the foreign key of C that points at T (Django's fk_name). It may be left
	// out when C has exactly one foreign key to T.
	ForeignKey func(child *C) *models.FK[T]
	// Name tells apart inlines of the same model and foreign key on one form, e.g. one per
	// QuerySet. It becomes part of the form field names, so use letters, digits and underscores.
	Name string
	// Label is the section title (default: the plural name of C).
	Label string
	// Help is shown under the title.
	Help string
	// Configure picks the columns; the probe references fields with typed pointers.
	Configure func(child *C, config *InlineConfig)
	// Stacked shows each row as a card of fields instead of a table row.
	Stacked bool
	// Extra is the number of blank rows offered for new objects; more can be added with a button.
	Extra int
	// ShowChangeLink links each row to its own change page.
	ShowChangeLink bool
	// QuerySet narrows the rows shown for parent (Django's get_queryset).
	QuerySet func(r *Request, parent *T, children query.QuerySet[C]) query.QuerySet[C]
	// Permissions overrides what may be done with the rows of parent, e.g. to stop adding rows to a
	// closed report. Nothing can be added, changed or deleted while parent itself is read-only.
	Permissions func(r *Request, parent *T, granted Permissions) Permissions
}

// InlineConfig collects the column options of an inline. It is filled by InlineAdmin.Configure.
type InlineConfig struct {
	fields           []Field
	readonly         []Field
	relatedQuerySets []relatedRule
}

// Fields sets the columns (default: every editable field except the foreign key to the parent).
func (c *InlineConfig) Fields(fields ...Field) { c.fields = fields }

// Readonly shows fields without allowing edits.
func (c *InlineConfig) Readonly(fields ...Field) { c.readonly = append(c.readonly, fields...) }

func (c *InlineConfig) addRelatedQuerySet(rule relatedRule) {
	c.relatedQuerySets = append(c.relatedQuerySets, rule)
}

func (a InlineAdmin[T, C]) editorFor(*T) (extraField, error) {
	child, ok := models.InfoFor[C]()
	if !ok {
		return nil, fmt.Errorf("model %s is not registered; call models.Register first", reflect.TypeFor[C]())
	}
	editor := &inlineEditor[T, C]{admin: a, child: child, readonly: map[*models.Field]bool{}, related: map[*models.Field]*relatedLookup{}}
	parentType := reflect.TypeFor[T]()
	if a.ForeignKey != nil {
		pointer := a.ForeignKey((*C)(child.Probe()))
		field, ok := child.FieldAt(unsafe.Pointer(pointer), reflect.TypeFor[models.FK[T]]())
		if !ok {
			return nil, fmt.Errorf("ForeignKey of %s does not return a declared field", child.Name)
		}
		editor.foreignKey = field
	} else {
		for _, field := range child.Fields {
			if field.Kind == models.KindForeignKey && field.RelatedType == parentType {
				if editor.foreignKey != nil {
					return nil, fmt.Errorf("%s has more than one foreign key to %s; set ForeignKey", child.Name, parentType.Name())
				}
				editor.foreignKey = field
			}
		}
		if editor.foreignKey == nil {
			return nil, fmt.Errorf("%s has no foreign key to %s", child.Name, parentType.Name())
		}
	}
	editor.prefix = child.Key() + "_" + editor.foreignKey.LookupName()
	if a.Name != "" {
		editor.prefix += "_" + a.Name
	}

	config := &InlineConfig{}
	if a.Configure != nil {
		a.Configure((*C)(child.Probe()), config)
	}
	local := func(kind string, reference Field) (*models.Field, error) {
		path, field, err := resolveField(child, reference)
		switch {
		case err != nil:
			return nil, fmt.Errorf("%s of %s: %w", kind, child.Name, err)
		case len(path) > 0:
			return nil, fmt.Errorf("%s of %s cannot use admin.Through", kind, child.Name)
		case field == editor.foreignKey:
			return nil, fmt.Errorf("%s of %s: %s is set to the parent automatically", kind, child.Name, field)
		}
		return field, nil
	}
	for _, reference := range config.fields {
		field, err := local("Fields", reference)
		if err != nil {
			return nil, err
		}
		editor.fields = append(editor.fields, field)
	}
	if len(config.fields) == 0 {
		for _, field := range child.Fields {
			if !field.Auto && field.Editable && field != editor.foreignKey && field.Kind != models.KindBytes {
				editor.fields = append(editor.fields, field)
			}
		}
	}
	for _, reference := range config.readonly {
		field, err := local("Readonly", reference)
		if err != nil {
			return nil, err
		}
		editor.readonly[field] = true
	}
	if len(config.relatedQuerySets) > 0 {
		parent, ok := models.InfoFor[T]()
		if !ok {
			return nil, fmt.Errorf("model %s is not registered; call models.Register first", parentType)
		}
		for _, rule := range config.relatedQuerySets {
			lookup, err := resolveRelatedRule(child, rule, lookupKey(parent, "inline/"+editor.prefix))
			if err != nil {
				return nil, fmt.Errorf("RelatedQuerySet of %s: %w", child.Name, err)
			}
			if editor.related[lookup.field] != nil {
				return nil, fmt.Errorf("RelatedQuerySet of %s: %s is set twice", child.Name, lookup.field)
			}
			editor.related[lookup.field] = lookup
		}
	}
	return editor, nil
}

type inlineEditor[T, C any] struct {
	admin      InlineAdmin[T, C]
	child      *models.ModelInfo
	foreignKey *models.Field
	fields     []*models.Field
	readonly   map[*models.Field]bool
	related    map[*models.Field]*relatedLookup
	prefix     string
}

func (e *inlineEditor[T, C]) relatedLookups() []*relatedLookup {
	lookups := make([]*relatedLookup, 0, len(e.related))
	for _, lookup := range e.related {
		lookups = append(lookups, lookup)
	}
	return lookups
}

// lookup narrows the rows a foreign key of the inline offers: the inline's RelatedQuerySet, else
// the one of C's admin.
func (e *inlineEditor[T, C]) lookup(r *Request, field *models.Field) *relatedLookup {
	if lookup := e.related[field]; lookup != nil {
		return lookup
	}
	if childEntry := r.Site.entryFor(e.child); childEntry != nil {
		return childEntry.config().related[field]
	}
	return nil
}

// maxInlineRows caps the rows read from one submitted inline.
const maxInlineRows = 1000

// inlineState is what the request may do with the rows of one parent.
type inlineState struct {
	permissions Permissions
	parentID    int64
	objects     []any
	byID        map[int64]any
	changeable  map[int64]bool
	deletable   map[int64]bool
}

// inlineRow is one row of the inline, as stored or as submitted.
type inlineRow struct {
	index    string
	id       int64
	object   any
	editable bool
	deleted  bool
	changed  bool
	locked   map[*models.Field]bool
	errors   map[string]string
	problems []string
}

func (e *inlineEditor[T, C]) name() string { return e.prefix }

// row wraps a child as stored. The Readonly and ReadonlyWhen fields of C's admin stay read-only.
func (e *inlineEditor[T, C]) row(r *Request, id int64, object any, editable bool) *inlineRow {
	row := &inlineRow{id: id, object: object, editable: editable, locked: map[*models.Field]bool{}, errors: map[string]string{}}
	if childEntry := r.Site.entryFor(e.child); childEntry != nil {
		row.locked = childEntry.config().lockedFields(r, object)
		for field := range childEntry.config().readonly {
			row.locked[field] = true
		}
	}
	return row
}

func (e *inlineEditor[T, C]) columnEditable(field *models.Field) bool {
	return !e.readonly[field] && !field.Auto && field.Editable && field.Kind != models.KindBytes
}

func (e *inlineEditor[T, C]) fieldEditable(row *inlineRow, field *models.Field) bool {
	return row.editable && !row.locked[field] && e.columnEditable(field)
}

func (e *inlineEditor[T, C]) load(r *Request, object any, isNew, editable bool) (*inlineState, error) {
	parent := object.(*T)
	state := &inlineState{parentID: models.MustInfo[T]().PKOf(object), byID: map[int64]any{}, changeable: map[int64]bool{}, deletable: map[int64]bool{}}
	childEntry := r.Site.entryFor(e.child)
	if childEntry != nil {
		state.permissions = r.Site.permissionsFor(r, childEntry)
	} else {
		state.permissions = modelPermissions(r, e.child)
	}
	if e.admin.Permissions != nil {
		state.permissions = e.admin.Permissions(r, parent, state.permissions)
	}
	if !editable || !state.permissions.View {
		state.permissions.Add, state.permissions.Change, state.permissions.Delete = false, false, false
	}
	if isNew || !state.permissions.View {
		return state, nil
	}

	children := query.Objects[C]()
	if childEntry != nil {
		children = query.FromDynamic[C](childEntry.viewQuerySet(r))
	}
	children = children.Filter(query.FieldLookup(e.foreignKey, "exact", state.parentID))
	if e.admin.QuerySet != nil {
		children = e.admin.QuerySet(r, parent, children)
	}
	visible := children.Dynamic()
	if len(e.child.Ordering) == 0 {
		visible = visible.OrderByNames(e.child.PK.LookupName())
	}
	ctx := r.Context()
	objects, err := visible.All(ctx)
	if err != nil {
		return nil, err
	}
	state.objects = objects
	ids := make([]int64, len(objects))
	for index, child := range objects {
		ids[index] = e.child.PKOf(child)
		state.byID[ids[index]] = child
	}
	allowed := func(granted bool, rule func(*Request) query.Dynamic) (map[int64]bool, error) {
		switch {
		case !granted:
			return map[int64]bool{}, nil
		case childEntry != nil:
			return allowedSet(ctx, visible.And(rule(r)), ids)
		}
		return allowedSet(ctx, visible, ids)
	}
	var changeRule, deleteRule func(*Request) query.Dynamic
	if childEntry != nil {
		changeRule, deleteRule = childEntry.changeQuerySet, childEntry.deleteQuerySet
	}
	if state.changeable, err = allowed(state.permissions.Change, changeRule); err != nil {
		return nil, err
	}
	state.deletable, err = allowed(state.permissions.Delete, deleteRule)
	return state, err
}

func (e *inlineEditor[T, C]) newRow(r *Request, parentID int64) *inlineRow {
	object := e.child.New()
	e.child.ApplyDefaults(object)
	_ = e.foreignKey.Set(object, parentID)
	return e.row(r, 0, object, true)
}

// bind reads the submitted rows. New rows that were left as they were offered are dropped, like
// Django's extra forms.
func (e *inlineEditor[T, C]) bind(r *Request, state *inlineState, form url.Values) []*inlineRow {
	total, _ := strconv.Atoi(form.Get(e.prefix + "-TOTAL_FORMS"))
	total = min(max(total, 0), maxInlineRows)
	var rows []*inlineRow
	for index := range total {
		key := fmt.Sprintf("%s-%d-", e.prefix, index)
		if !form.Has(key + "id") {
			continue
		}
		var row *inlineRow
		if id, err := strconv.ParseInt(form.Get(key+"id"), 10, 64); err == nil && id > 0 {
			object, ok := state.byID[id]
			if !ok {
				continue
			}
			row = e.row(r, id, object, state.changeable[id])
			row.deleted = state.deletable[id] && form.Has(key+"DELETE")
		} else {
			if !state.permissions.Add {
				continue
			}
			row = e.newRow(r, state.parentID)
		}
		row.index = strconv.Itoa(index)
		if !row.deleted {
			e.bindRow(r, row, form, key)
		}
		rows = append(rows, row)
	}
	return rows
}

func (e *inlineEditor[T, C]) bindRow(r *Request, row *inlineRow, form url.Values, key string) {
	for _, field := range e.fields {
		if !e.fieldEditable(row, field) {
			continue
		}
		name := key + field.LookupName()
		before := formatInput(field, row.object)
		if message := r.Site.bindValue(r, field, row.object, form, name, e.lookup(r, field)); message != "" {
			row.errors[name] = message
			row.changed = row.changed || form.Get(name) != before
			continue
		}
		row.changed = row.changed || formatInput(field, row.object) != before
	}
	if !row.changed {
		clear(row.errors)
		return
	}
	if validator, ok := row.object.(query.Validator); ok && len(row.errors) == 0 {
		if err := validator.Validate(); err != nil {
			row.problems = append(row.problems, r.Site.errorText(err))
		}
	}
}

func (e *inlineEditor[T, C]) validate(r *Request, object any, isNew bool, form url.Values) string {
	state, err := e.load(r, object, isNew, true)
	if err != nil {
		log.Printf("admin: loading inline %s: %v", e.prefix, err)
		return "Something went wrong. The error was logged."
	}
	for _, row := range e.bind(r, state, form) {
		if len(row.errors) > 0 || len(row.problems) > 0 {
			return "Please correct the errors below."
		}
	}
	return ""
}

func (e *inlineEditor[T, C]) save(r *Request, object any, form url.Values) error {
	state, err := e.load(r, object, false, true)
	if err != nil {
		return err
	}
	ctx := r.Context()
	childEntry := r.Site.entryFor(e.child)
	for _, row := range e.bind(r, state, form) {
		switch {
		case row.deleted:
			if err := query.DeleteAny(ctx, e.child, row.object); err != nil {
				return err
			}
		case row.changed:
			if err := e.saveRow(r, childEntry, row); err != nil {
				return err
			}
		}
	}
	return nil
}

// saveRow saves a changed row with the BeforeSave and AfterSave hooks of C's admin.
func (e *inlineEditor[T, C]) saveRow(r *Request, childEntry modelEntry, row *inlineRow) error {
	isNew := row.id == 0
	if childEntry != nil {
		if err := childEntry.beforeSave(r, row.object, isNew); err != nil {
			return userError{err}
		}
	}
	if err := query.SaveObject(r.Context(), e.child, row.object); err != nil {
		return err
	}
	if childEntry != nil {
		if err := childEntry.afterSave(r, row.object, isNew); err != nil {
			return userError{err}
		}
	}
	return nil
}

// ---- rendering ----

type inlineView struct {
	Stacked bool
	Headers []inlineHeader
	Rows    []inlineFormRow
	// Empty is the blank row cloned by the add button; nil when rows cannot be added.
	Empty     *inlineFormRow
	TotalName string
	Total     int
	AddLabel  string
}

type inlineHeader struct {
	Label, Help string
	Required    bool
}

type inlineFormRow struct {
	Key, Label, URL         string
	ID                      int64
	Fields                  []formField
	Errors                  []string
	New, CanDelete, Deleted bool
}

func (e *inlineEditor[T, C]) render(r *Request, object any, isNew, editable bool, submitted url.Values) (extraView, error) {
	label := e.admin.Label
	if label == "" {
		label = e.child.VerboseNamePlural
	}
	view := extraView{Name: e.prefix, Label: label, Help: e.admin.Help, Widget: "inline", ReadOnly: !editable}
	state, err := e.load(r, object, isNew, editable)
	if err != nil || !state.permissions.View {
		view.Hidden = true
		return view, err
	}

	var rows []*inlineRow
	if submitted != nil {
		rows = e.bind(r, state, submitted)
	} else {
		for _, child := range state.objects {
			id := e.child.PKOf(child)
			rows = append(rows, e.row(r, id, child, state.changeable[id]))
		}
		if state.permissions.Add {
			for range e.admin.Extra {
				rows = append(rows, e.newRow(r, state.parentID))
			}
		}
		for index, row := range rows {
			row.index = strconv.Itoa(index)
		}
	}
	objects := make([]any, len(rows))
	for index, row := range rows {
		objects[index] = row.object
	}
	if err := e.preload(r, objects); err != nil {
		return view, err
	}

	site := r.Site
	inline := &inlineView{Stacked: e.admin.Stacked, TotalName: e.prefix + "-TOTAL_FORMS", AddLabel: site.addTitle(e.child)}
	for _, field := range e.fields {
		inline.Headers = append(inline.Headers, inlineHeader{
			Label: site.translate(field.VerboseName), Help: site.translate(field.HelpText),
			Required: e.columnEditable(field) && !field.Blank && !field.Nullable && field.Kind != models.KindBool,
		})
	}
	childEntry := site.entryFor(e.child)
	for _, row := range rows {
		rendered := e.renderRow(r, row, submitted)
		if row.id != 0 {
			rendered.CanDelete = state.deletable[row.id]
			if e.admin.ShowChangeLink && childEntry != nil {
				rendered.URL = site.objectURL(e.child, row.id)
			}
		}
		inline.Rows = append(inline.Rows, rendered)
		if index, _ := strconv.Atoi(row.index); index >= inline.Total {
			inline.Total = index + 1
		}
	}
	if state.permissions.Add {
		blank := e.newRow(r, state.parentID)
		blank.index = "__prefix__"
		empty := e.renderRow(r, blank, nil)
		inline.Empty = &empty
	}
	view.Inline = inline
	return view, nil
}

// preload loads what the labels of the rows and their read-only foreign keys need.
func (e *inlineEditor[T, C]) preload(r *Request, objects []any) error {
	ctx := r.Context()
	if err := loadLabels(ctx, e.child, objects); err != nil {
		return err
	}
	for _, field := range e.fields {
		if field.Kind != models.KindForeignKey {
			continue
		}
		if err := query.PreloadRelated(ctx, objects, field); err != nil {
			return err
		}
		if err := loadLabels(ctx, field.Related(), loadedTargets(field, objects)); err != nil {
			return err
		}
	}
	return nil
}

func (e *inlineEditor[T, C]) renderRow(r *Request, row *inlineRow, submitted url.Values) inlineFormRow {
	site := r.Site
	key := e.prefix + "-" + row.index + "-"
	rendered := inlineFormRow{Key: key, ID: row.id, New: row.id == 0, Deleted: row.deleted, Errors: row.problems}
	if rendered.New {
		rendered.Label = site.translatef("New %s", strings.ToLower(site.modelName(e.child)))
	} else {
		rendered.Label = e.child.DisplayString(row.object)
	}
	for _, field := range e.fields {
		name := key + field.LookupName()
		rendered.Fields = append(rendered.Fields, site.buildFormField(r, field, name, row.object, e.fieldEditable(row, field), submitted, row.errors[name], e.lookup(r, field), false))
	}
	return rendered
}
