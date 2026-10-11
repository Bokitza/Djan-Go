package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/yoav/djan-go/auth"
	"github.com/yoav/djan-go/db"
	"github.com/yoav/djan-go/models"
	"github.com/yoav/djan-go/query"
)

type modelMeta struct {
	Label, Plural, App, URL, AddURL, Icon string
	Permissions                           permissions
}

func (s *Site) meta(r *Request, entry modelEntry) modelMeta {
	info := entry.info()
	permissions := s.permissionsFor(r, entry)
	meta := modelMeta{Label: info.VerboseName, Plural: info.VerboseNamePlural, App: titleCase(info.App), URL: s.modelURL(info), Icon: entry.icon(), Permissions: permissions}
	if permissions.Add {
		meta.AddURL = s.modelURL(info) + "add/"
	}
	return meta
}

func (s *Site) modelCrumbs(entry modelEntry, extra ...crumb) []crumb {
	info := entry.info()
	crumbs := []crumb{{Label: titleCase(info.App)}, {Label: info.VerboseNamePlural, URL: s.modelURL(info)}}
	return append(crumbs, extra...)
}

// ---- dashboard ----

type dashboardCard struct {
	Meta  modelMeta
	Count int64
}

type dashboardGroup struct {
	Label string
	Cards []dashboardCard
}

func (s *Site) dashboard(w http.ResponseWriter, r *Request) {
	var groups []dashboardGroup
	for _, entry := range s.sortedEntries() {
		meta := s.meta(r, entry)
		if !meta.Permissions.View {
			continue
		}
		count, err := entry.viewQuerySet(r).Count(r.Context())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if len(groups) == 0 || groups[len(groups)-1].Label != meta.App {
			groups = append(groups, dashboardGroup{Label: meta.App})
		}
		groups[len(groups)-1].Cards = append(groups[len(groups)-1].Cards, dashboardCard{Meta: meta, Count: count})
	}
	data := s.newPage(w, r, "Dashboard", "", nil, map[string]any{"Groups": groups})
	s.render(w, "dashboard.html", "base", http.StatusOK, data)
}

// ---- list ----

type listHeader struct {
	Label, SortURL string
	Sortable       bool
	Sorted         bool
	Descending     bool
}

type listRow struct {
	ID                   int64
	URL, DeleteURL       string
	Cells                []cell
	CanChange, CanDelete bool
}

type filterOption struct {
	Label, URL string
	Active     bool
}

type filterGroup struct {
	Label   string
	Active  bool
	Options []filterOption
}

type activeFilter struct{ Label, RemoveURL string }

type pageLink struct {
	Number   int
	URL      string
	Active   bool
	Ellipsis bool
}

type listPage struct {
	Meta          modelMeta
	Headers       []listHeader
	Rows          []listRow
	Search        string
	HasSearch     bool
	Filters       []filterGroup
	ActiveFilters []activeFilter
	Total         int64
	From, To      int
	Page, Pages   int
	PrevURL       string
	NextURL       string
	PageLinks     []pageLink
	ListURL       string
	BulkDeleteURL string
	ClearURL      string
	Hidden        []hiddenParameter
}

type hiddenParameter struct{ Name, Value string }

func listState(values url.Values) []hiddenParameter {
	var hidden []hiddenParameter
	for name, items := range values {
		if name == "q" || name == "p" {
			continue
		}
		for _, item := range items {
			hidden = append(hidden, hiddenParameter{Name: name, Value: item})
		}
	}
	slices.SortFunc(hidden, func(a, b hiddenParameter) int { return strings.Compare(a.Name, b.Name) })
	return hidden
}

func searchCond(fields []*models.Field, search string) query.Cond {
	var terms []query.Cond
	for _, term := range strings.Fields(search) {
		var alternatives []query.Cond
		for _, field := range fields {
			alternatives = append(alternatives, query.FieldLookup(field, "icontains", term))
		}
		terms = append(terms, query.Or(alternatives...))
	}
	return query.And(terms...)
}

const filterPrefix = "f."

func startOfDay(moment time.Time) time.Time {
	year, month, day := moment.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, moment.Location())
}

func (s *Site) filterOptions(r *Request, entry modelEntry, field *models.Field) []choiceOption {
	ctx := r.Context()
	switch {
	case field.Kind == models.KindBool:
		options := []choiceOption{{Value: "1", Label: "Yes"}, {Value: "0", Label: "No"}}
		if field.Nullable {
			options = append(options, choiceOption{Value: "null", Label: "Unknown"})
		}
		return options
	case len(field.Choices) > 0:
		var options []choiceOption
		for _, choice := range field.Choices {
			options = append(options, choiceOption{Value: fmt.Sprint(choice), Label: models.ChoiceLabel(choice)})
		}
		return options
	case field.Kind == models.KindDateTime:
		return []choiceOption{{Value: "today", Label: "Today"}, {Value: "7d", Label: "Past 7 days"}, {Value: "month", Label: "This month"}, {Value: "year", Label: "This year"}}
	case field.Kind == models.KindForeignKey:
		related := field.Related()
		relatedEntry := s.entryFor(related)
		queryset := query.ForModel(related)
		if relatedEntry != nil {
			if !s.permissionsFor(r, relatedEntry).View {
				return nil
			}
			queryset = relatedEntry.viewQuerySet(r)
		} else if !r.User.IsSuperuser {
			return nil
		}
		objects, err := queryset.Limit(50).All(ctx)
		if err != nil {
			return nil
		}
		var options []choiceOption
		for _, object := range objects {
			options = append(options, choiceOption{Value: strconv.FormatInt(related.PKOf(object), 10), Label: related.DisplayString(object)})
		}
		if field.Nullable {
			options = append(options, choiceOption{Value: "null", Label: "None"})
		}
		return options
	}
	values, err := entry.viewQuerySet(r).OrderByNames(field.LookupName()).Limit(500).Values(ctx, field.LookupName())
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var options []choiceOption
	for _, row := range values {
		value := row[field.LookupName()]
		if value == nil {
			if !seen["null"] {
				seen["null"] = true
				options = append(options, choiceOption{Value: "null", Label: "None"})
			}
			continue
		}
		text := fmt.Sprint(value)
		if !seen[text] && len(options) < 30 {
			seen[text] = true
			options = append(options, choiceOption{Value: text, Label: text})
		}
	}
	return options
}

func filterCond(field *models.Field, raw string) (query.Cond, bool) {
	if raw == "null" {
		return query.FieldLookup(field, "isnull", true), true
	}
	switch field.Kind {
	case models.KindBool:
		return query.FieldLookup(field, "exact", raw == "1"), raw == "1" || raw == "0"
	case models.KindDateTime:
		now := time.Now()
		var start time.Time
		switch raw {
		case "today":
			start = startOfDay(now)
		case "7d":
			start = startOfDay(now).AddDate(0, 0, -7)
		case "month":
			start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		case "year":
			start = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
		default:
			return nil, false
		}
		return query.FieldLookup(field, "gte", start), true
	}
	value, err := parseInput(field, raw, true)
	if err != nil || value == nil {
		return nil, false
	}
	return query.FieldLookup(field, "exact", value), true
}

func (s *Site) listView(w http.ResponseWriter, r *Request, entry modelEntry) {
	ctx := r.Context()
	info, config := entry.info(), entry.config()
	values := r.URL.Query()
	page := listPage{Meta: s.meta(r, entry), HasSearch: len(config.searchFields) > 0, ListURL: s.modelURL(info),
		BulkDeleteURL: s.modelURL(info) + "bulk-delete/", Hidden: listState(values)}
	queryset := entry.viewQuerySet(r)

	page.Search = strings.TrimSpace(values.Get("q"))
	if page.Search != "" && page.HasSearch {
		queryset = queryset.Filter(searchCond(config.searchFields, page.Search))
	}

	var dropOnClear []string
	for _, field := range config.filters {
		parameter := filterPrefix + field.LookupName()
		dropOnClear = append(dropOnClear, parameter)
		current := values.Get(parameter)
		group := filterGroup{Label: field.VerboseName, Options: []filterOption{{Label: "All", URL: withQuery(values, parameter, "", "p"), Active: current == ""}}}
		for _, option := range s.filterOptions(r, entry, field) {
			active := option.Value == current
			group.Options = append(group.Options, filterOption{Label: option.Label, URL: withQuery(values, parameter, option.Value, "p"), Active: active})
			if active {
				group.Active = true
				page.ActiveFilters = append(page.ActiveFilters, activeFilter{Label: field.VerboseName + ": " + option.Label, RemoveURL: withQuery(values, parameter, "", "p")})
			}
		}
		if current != "" {
			if cond, ok := filterCond(field, current); ok {
				queryset = queryset.Filter(cond)
			}
		}
		page.Filters = append(page.Filters, group)
	}
	page.ClearURL = withQuery(values, "", "", append(dropOnClear, "p", "q")...)

	order := values.Get("o")
	sortKey := strings.TrimPrefix(order, "-")
	validSort := false
	for _, column := range config.columns {
		validSort = validSort || (column.sortKey() != "" && column.sortKey() == sortKey)
	}
	if order != "" && validSort {
		queryset = queryset.OrderByNames(order)
	} else if len(config.ordering) > 0 {
		queryset = queryset.OrderByNames(config.ordering...)
	} else if len(info.Ordering) == 0 {
		queryset = queryset.OrderByNames("-" + info.PK.LookupName())
	}
	for _, column := range config.columns {
		header := listHeader{Label: column.label}
		if key := column.sortKey(); key != "" {
			header.Sortable = true
			header.Sorted = sortKey == key && validSort
			header.Descending = header.Sorted && strings.HasPrefix(order, "-")
			next := key
			if header.Sorted && !header.Descending {
				next = "-" + key
			}
			header.SortURL = withQuery(values, "o", next, "p")
		}
		page.Headers = append(page.Headers, header)
	}

	total, err := queryset.Count(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	page.Total = total
	page.Pages = max(1, int((total+int64(config.perPage)-1)/int64(config.perPage)))
	page.Page, _ = strconv.Atoi(values.Get("p"))
	page.Page = min(max(page.Page, 1), page.Pages)
	offset := (page.Page - 1) * config.perPage
	objects, err := queryset.Limit(config.perPage).Offset(offset).All(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(objects) > 0 {
		page.From, page.To = offset+1, offset+len(objects)
	}
	if page.Page > 1 {
		page.PrevURL = withQuery(values, "p", strconv.Itoa(page.Page-1))
	}
	if page.Page < page.Pages {
		page.NextURL = withQuery(values, "p", strconv.Itoa(page.Page+1))
	}
	for number := 1; number <= page.Pages; number++ {
		if number == 1 || number == page.Pages || (number >= page.Page-2 && number <= page.Page+2) {
			page.PageLinks = append(page.PageLinks, pageLink{Number: number, URL: withQuery(values, "p", strconv.Itoa(number)), Active: number == page.Page})
		} else if last := len(page.PageLinks) - 1; last >= 0 && !page.PageLinks[last].Ellipsis {
			page.PageLinks = append(page.PageLinks, pageLink{Ellipsis: true})
		}
	}

	rows, err := s.buildRows(r, entry, objects, page.Meta.Permissions)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	page.Rows = rows

	data := s.newPage(w, r, info.VerboseNamePlural, info.Label(), s.modelCrumbs(entry), page)
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "results" {
		s.render(w, "list.html", "results", http.StatusOK, data)
		return
	}
	s.render(w, "list.html", "base", http.StatusOK, data)
}

func idValues(ids []int64) []any {
	values := make([]any, len(ids))
	for index, id := range ids {
		values[index] = id
	}
	return values
}

func allowedSet(ctx context.Context, queryset query.Dynamic, ids []int64) (map[int64]bool, error) {
	allowed := map[int64]bool{}
	if len(ids) == 0 {
		return allowed, nil
	}
	keys, err := queryset.Filter(query.FieldLookup(queryset.Model().PK, "in", idValues(ids)...)).OrderByNames().PrimaryKeys(ctx)
	for _, key := range keys {
		allowed[key] = true
	}
	return allowed, err
}

func (s *Site) buildRows(r *Request, entry modelEntry, objects []any, permissions permissions) ([]listRow, error) {
	ctx := r.Context()
	info := entry.info()
	ids := make([]int64, len(objects))
	for index, object := range objects {
		ids[index] = info.PKOf(object)
	}
	changeable, deletable := map[int64]bool{}, map[int64]bool{}
	var err error
	if permissions.Change {
		if changeable, err = allowedSet(ctx, entry.changeQuerySet(r), ids); err != nil {
			return nil, err
		}
	}
	if permissions.Delete {
		if deletable, err = allowedSet(ctx, entry.deleteQuerySet(r), ids); err != nil {
			return nil, err
		}
	}
	rows := make([]listRow, len(objects))
	for index, id := range ids {
		rows[index] = listRow{ID: id, URL: s.objectURL(info, id), DeleteURL: s.objectURL(info, id) + "delete/", CanChange: changeable[id], CanDelete: deletable[id]}
	}
	for _, column := range entry.config().columns {
		cells, err := columnCells(ctx, s, r, info, objects, column)
		if err != nil {
			return nil, err
		}
		for index := range rows {
			rows[index].Cells = append(rows[index].Cells, cells[index])
		}
	}
	return rows, nil
}

// ---- add / change ----

type formPage struct {
	Meta          modelMeta
	IsNew         bool
	Editable      bool
	ObjectLabel   string
	Fields        []formField
	Extras        []extraView
	Errors        []string
	Action        string
	DeleteURL     string
	CanDelete     bool
	ID            int64
	CreatedAt     string
	UpdatedAt     string
	ReadOnlyNotes string
}

func (s *Site) addView(w http.ResponseWriter, r *Request, entry modelEntry) {
	info := entry.info()
	if !s.permissionsFor(r, entry).Add {
		s.renderError(w, r, http.StatusForbidden, "You don't have permission to add "+info.VerboseNamePlural+".")
		return
	}
	object := info.New()
	info.ApplyDefaults(object)
	s.handleForm(w, r, entry, object, true, true)
}

func (s *Site) changeView(w http.ResponseWriter, r *Request, entry modelEntry) {
	info := entry.info()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Not found.")
		return
	}
	object, err := entry.viewQuerySet(r).GetByPK(r.Context(), id)
	if errors.Is(err, query.ErrNotFound) {
		s.renderError(w, r, http.StatusNotFound, fmt.Sprintf("%s with ID %d doesn't exist or you can't see it.", info.VerboseName, id))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	editable := false
	if s.permissionsFor(r, entry).Change {
		allowed, err := allowedSet(r.Context(), entry.changeQuerySet(r), []int64{id})
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		editable = allowed[id]
	}
	if r.Method == http.MethodPost && !editable {
		s.renderError(w, r, http.StatusForbidden, "You don't have permission to change this "+strings.ToLower(info.VerboseName)+".")
		return
	}
	s.handleForm(w, r, entry, object, false, editable)
}

func (s *Site) handleForm(w http.ResponseWriter, r *Request, entry modelEntry, object any, isNew, editable bool) {
	info := entry.info()
	var submitted url.Values
	fieldErrors := map[string]string{}
	var formErrors []string
	extraErrors := map[string]string{}

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.renderError(w, r, http.StatusBadRequest, "Invalid form.")
			return
		}
		submitted = r.PostForm
		fieldErrors = s.bindForm(r, entry, object, submitted)
		for _, extra := range entry.extraFields() {
			if message := extra.validate(r, isNew, submitted); message != "" {
				extraErrors[extra.name()] = message
			}
		}
		if len(fieldErrors) == 0 && len(extraErrors) == 0 {
			if validator, ok := object.(query.Validator); ok {
				if err := validator.Validate(); err != nil {
					formErrors = append(formErrors, err.Error())
				}
			}
		}
		if len(fieldErrors) == 0 && len(extraErrors) == 0 && len(formErrors) == 0 {
			err := db.Atomic(r.Context(), func(ctx context.Context) error {
				transactional := &Request{Request: r.Request.WithContext(ctx), User: r.User, Site: s}
				if err := entry.beforeSave(transactional, object, isNew); err != nil {
					return userError{err}
				}
				if err := query.SaveObject(ctx, info, object); err != nil {
					return err
				}
				for _, extra := range entry.extraFields() {
					if err := extra.save(transactional, object, submitted); err != nil {
						return err
					}
				}
				if err := entry.afterSave(transactional, object, isNew); err != nil {
					return userError{err}
				}
				return nil
			})
			if err == nil {
				label := info.DisplayString(object)
				verb := "changed"
				if isNew {
					verb = "added"
				}
				s.addFlash(w, r.Request, "success", fmt.Sprintf("The %s “%s” was %s successfully.", strings.ToLower(info.VerboseName), label, verb))
				id := info.PKOf(object)
				switch submitted.Get("_action") {
				case "continue":
					redirect(w, r.Request, s.objectURL(info, id))
				case "another":
					redirect(w, r.Request, s.modelURL(info)+"add/")
				default:
					redirect(w, r.Request, s.modelURL(info))
				}
				return
			}
			message, ok := friendlyError(err)
			if !ok {
				s.serverError(w, r, err)
				return
			}
			formErrors = append(formErrors, message)
		}
	}

	id := info.PKOf(object)
	page := formPage{Meta: s.meta(r, entry), IsNew: isNew, Editable: editable, ObjectLabel: info.DisplayString(object), ID: id, Errors: formErrors}
	page.Fields = s.buildFormFields(r, entry, object, editable, submitted, fieldErrors)
	for _, extra := range entry.extraFields() {
		view, err := extra.render(r, object, isNew, editable, submitted)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		view.Error = extraErrors[extra.name()]
		page.Extras = append(page.Extras, view)
	}
	title := "Add " + strings.ToLower(info.VerboseName)
	crumbs := s.modelCrumbs(entry, crumb{Label: title})
	if isNew {
		page.Action = s.modelURL(info) + "add/"
	} else {
		page.Action = s.objectURL(info, id)
		title = page.ObjectLabel
		crumbs = s.modelCrumbs(entry, crumb{Label: page.ObjectLabel})
		if createdAt := info.FieldByName("created_at"); createdAt != nil {
			page.CreatedAt = displayValue(s, r, createdAt, object).Text
			page.UpdatedAt = displayValue(s, r, info.FieldByName("updated_at"), object).Text
		}
		if page.Meta.Permissions.Delete {
			allowed, err := allowedSet(r.Context(), entry.deleteQuerySet(r), []int64{id})
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			page.CanDelete = allowed[id]
			page.DeleteURL = s.objectURL(info, id) + "delete/"
		}
		if !editable {
			page.ReadOnlyNotes = "You can view this " + strings.ToLower(info.VerboseName) + " but not change it."
		}
	}
	status := http.StatusOK
	if r.Method == http.MethodPost && r.Header.Get("HX-Request") != "true" {
		status = http.StatusUnprocessableEntity
	}
	s.render(w, "form.html", "base", status, s.newPage(w, r, title, info.Label(), crumbs, page))
}

type userError struct{ err error }

func (e userError) Error() string { return e.err.Error() }

func friendlyError(err error) (string, bool) {
	var hookError userError
	if errors.As(err, &hookError) {
		return hookError.Error(), true
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "23505":
			return "This would create a duplicate: " + strings.TrimSpace(databaseError.Detail), true
		case "23503":
			return "This object is referenced by other objects or references a missing one: " + strings.TrimSpace(databaseError.Detail), true
		case "23502", "23514", "22001":
			return databaseError.Message, true
		}
	}
	return "", false
}

// ---- delete ----

func (s *Site) deleteView(w http.ResponseWriter, r *Request, entry modelEntry) {
	info := entry.info()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Not found.")
		return
	}
	if !s.permissionsFor(r, entry).Delete {
		s.renderError(w, r, http.StatusForbidden, "You don't have permission to delete "+info.VerboseNamePlural+".")
		return
	}
	object, err := entry.deleteQuerySet(r).GetByPK(r.Context(), id)
	if errors.Is(err, query.ErrNotFound) {
		if _, viewErr := entry.viewQuerySet(r).GetByPK(r.Context(), id); viewErr == nil {
			s.renderError(w, r, http.StatusForbidden, "You don't have permission to delete this "+strings.ToLower(info.VerboseName)+".")
			return
		}
		s.renderError(w, r, http.StatusNotFound, "Not found.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	label := info.DisplayString(object)
	if err := query.DeleteAny(r.Context(), info, object); err != nil {
		if message, ok := friendlyError(err); ok {
			s.addFlash(w, r.Request, "error", "Could not delete “"+label+"”. "+message)
			redirect(w, r.Request, s.objectURL(info, id))
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.addFlash(w, r.Request, "success", fmt.Sprintf("The %s “%s” was deleted.", strings.ToLower(info.VerboseName), label))
	redirect(w, r.Request, s.modelURL(info))
}

func (s *Site) bulkDelete(w http.ResponseWriter, r *Request, entry modelEntry) {
	info := entry.info()
	if !s.permissionsFor(r, entry).Delete {
		s.renderError(w, r, http.StatusForbidden, "You don't have permission to delete "+info.VerboseNamePlural+".")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Invalid form.")
		return
	}
	var ids []int64
	for _, raw := range r.PostForm["ids"] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	back := s.modelURL(info) + "?" + r.URL.RawQuery
	if len(ids) == 0 {
		s.addFlash(w, r.Request, "info", "No items were selected.")
		redirect(w, r.Request, back)
		return
	}
	deleted, err := entry.deleteQuerySet(r).Filter(query.FieldLookup(info.PK, "in", idValues(ids)...)).OrderByNames().Delete(r.Context())
	if err != nil {
		if message, ok := friendlyError(err); ok {
			s.addFlash(w, r.Request, "error", "Nothing was deleted. "+message)
			redirect(w, r.Request, back)
			return
		}
		s.serverError(w, r, err)
		return
	}
	message := fmt.Sprintf("Deleted %d %s.", deleted, strings.ToLower(pluralFor(info, deleted)))
	level := "success"
	if skipped := int64(len(ids)) - deleted; skipped > 0 {
		message += fmt.Sprintf(" %d could not be deleted because you are not allowed to delete them.", skipped)
		level = "warning"
	}
	s.addFlash(w, r.Request, level, message)
	redirect(w, r.Request, back)
}

func pluralFor(info *models.ModelInfo, count int64) string {
	if count == 1 {
		return info.VerboseName
	}
	return info.VerboseNamePlural
}

// ---- autocomplete ----

type autocompleteOption struct {
	ID    int64
	Label string
}

func (s *Site) autocomplete(w http.ResponseWriter, r *Request, entry modelEntry) {
	info := entry.info()
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	queryset := entry.viewQuerySet(r)
	if search != "" {
		fields := entry.config().searchFields
		if len(fields) == 0 {
			for _, field := range info.Fields {
				if field.Kind == models.KindChar {
					fields = append(fields, field)
				}
			}
		}
		conds := []query.Cond{searchCond(fields, search)}
		if id, err := strconv.ParseInt(search, 10, 64); err == nil {
			conds = append(conds, query.FieldLookup(info.PK, "exact", id))
		}
		queryset = queryset.Filter(query.Or(conds...))
	}
	objects, err := queryset.Limit(20).All(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	options := make([]autocompleteOption, len(objects))
	for index, object := range objects {
		options[index] = autocompleteOption{ID: info.PKOf(object), Label: info.DisplayString(object)}
	}
	s.render(w, "list.html", "autocomplete", http.StatusOK, pageData{Page: options})
}

// ---- login / logout ----

func (s *Site) safeNext(next string) string {
	if strings.HasPrefix(next, s.Prefix+"/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") {
		return next
	}
	return s.Prefix + "/"
}

func (s *Site) loginPage(w http.ResponseWriter, r *http.Request) {
	if user := auth.CurrentUser(r); user != nil && user.IsStaff {
		http.Redirect(w, r, s.safeNext(r.URL.Query().Get("next")), http.StatusFound)
		return
	}
	s.renderLogin(w, r, http.StatusOK, "", "")
}

func (s *Site) renderLogin(w http.ResponseWriter, r *http.Request, status int, username, message string) {
	data := s.newPage(w, &Request{Request: r, Site: s}, "Log in", "", nil, map[string]any{
		"Username": username, "Error": message, "Next": s.safeNext(r.FormValue("next")),
	})
	s.render(w, "login.html", "base", status, data)
}

func (s *Site) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, r, http.StatusBadRequest, "", "Invalid form.")
		return
	}
	username := strings.TrimSpace(r.PostForm.Get("username"))
	user, err := auth.Authenticate(r.Context(), username, r.PostForm.Get("password"))
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			s.serverError(w, &Request{Request: r, Site: s}, err)
			return
		}
		s.renderLogin(w, r, http.StatusUnauthorized, username, "Please enter the correct username and password for a staff account. Both fields may be case-sensitive.")
		return
	}
	if !user.IsStaff {
		s.renderLogin(w, r, http.StatusForbidden, username, "This account doesn't have access to the admin.")
		return
	}
	if err := auth.Login(r.Context(), w, r, user); err != nil {
		s.serverError(w, &Request{Request: r, Site: s}, err)
		return
	}
	http.Redirect(w, r, s.safeNext(r.PostForm.Get("next")), http.StatusSeeOther)
}

func (s *Site) logout(w http.ResponseWriter, r *http.Request) {
	auth.Logout(r.Context(), w, r)
	s.addFlash(w, r, "info", "You have been logged out.")
	http.Redirect(w, r, s.Prefix+"/login/", http.StatusSeeOther)
}
