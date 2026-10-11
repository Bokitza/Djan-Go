package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yoav/djan-go/importexport"
	"github.com/yoav/djan-go/models"
	"github.com/yoav/djan-go/query"
)

// ImportExport is the import and export policy of a model, like django-import-export's
// ImportExportModelAdmin, ImportMixin, ExportMixin and ExportActionModelAdmin. Imports and
// exports follow the same rules as the admin forms: exports only contain rows the user can view,
// imported rows need the add permission (new rows) or must be in ChangeQuerySet (existing rows),
// and the ModelAdmin's BeforeSave and AfterSave hooks run for every imported object.
type ImportExport[T any] struct {
	// Import adds an import page: upload a file, preview a dry run, then confirm it.
	Import bool
	// Export adds an export menu that downloads the list with its current search and filters.
	Export bool
	// ExportSelected lets users export the rows they select in the list.
	ExportSelected bool
	// Resources are offered to the user, who picks one when there are several. Defaults to a
	// resource with every field, matched by ID.
	Resources []importexport.Resource[T]
	// ResourcesFor overrides Resources per request; returning none hides import and export.
	ResourcesFor func(r *Request) []importexport.Resource[T]
	// Formats are the file formats offered (defaults to CSV, TSV and JSON).
	Formats []importexport.Format
}

// maxImportSize caps uploaded import files.
const maxImportSize = 10 << 20

type requestKey struct{}

// RequestFrom returns the admin request behind an import or export, so resource hooks can check
// the user: admin.RequestFrom(ctx).User. Use the hook's ctx, not the request's, for queries.
// It returns nil outside the admin.
func RequestFrom(ctx context.Context) *Request {
	r, _ := ctx.Value(requestKey{}).(*Request)
	return r
}

// transferPolicy is what a request may import or export for a model.
type transferPolicy struct {
	Import, Export, ExportSelected bool
	Resources                      []transferResource
	Formats                        []importexport.Format
}

type transferResource struct {
	Index   int
	Name    string
	Headers []string
	Error   string
}

func (p transferPolicy) format(name string) importexport.Format {
	for _, format := range p.Formats {
		if format.Name() == name {
			return format
		}
	}
	return nil
}

func (p transferPolicy) formatForFile(filename string) importexport.Format {
	return p.format(strings.ToLower(strings.TrimPrefix(path.Ext(filename), ".")))
}

func (p transferPolicy) extensions() string {
	names := make([]string, len(p.Formats))
	for index, format := range p.Formats {
		names[index] = "." + format.Name()
	}
	return strings.Join(names, ",")
}

func (e *entry[T]) resources(r *Request) []importexport.Resource[T] {
	policy := e.admin.ImportExport
	switch {
	case policy == nil:
		return nil
	case policy.ResourcesFor != nil:
		return policy.ResourcesFor(r)
	case len(policy.Resources) > 0:
		return policy.Resources
	}
	return []importexport.Resource[T]{{}}
}

func (e *entry[T]) transfer(r *Request) transferPolicy {
	policy := e.admin.ImportExport
	resources := e.resources(r)
	if policy == nil || len(resources) == 0 {
		return transferPolicy{}
	}
	permissions := r.Site.permissionsFor(r, e)
	transfer := transferPolicy{
		Import:         policy.Import && (permissions.Add || permissions.Change),
		Export:         policy.Export && permissions.View,
		ExportSelected: policy.ExportSelected && permissions.View,
		Formats:        policy.Formats,
	}
	if len(transfer.Formats) == 0 {
		transfer.Formats = []importexport.Format{importexport.CSV, importexport.TSV, importexport.JSON}
	}
	for index, resource := range resources {
		name := resource.Name
		if name == "" {
			name = "Default"
		}
		described := transferResource{Index: index, Name: name}
		headers, err := resource.Headers()
		if err != nil {
			described.Error = err.Error()
		}
		described.Headers = headers
		transfer.Resources = append(transfer.Resources, described)
	}
	return transfer
}

func (e *entry[T]) resource(r *Request, index int) (importexport.Resource[T], error) {
	resources := e.resources(r)
	if index < 0 || index >= len(resources) {
		return importexport.Resource[T]{}, errors.New("unknown resource")
	}
	return resources[index], nil
}

func (e *entry[T]) exportDataset(r *Request, index int, queryset query.Dynamic) (*importexport.Dataset, error) {
	resource, err := e.resource(r, index)
	if err != nil {
		return nil, err
	}
	return resource.Export(context.WithValue(r.Context(), requestKey{}, r), query.FromDynamic[T](queryset))
}

// importDataset runs the resource with the admin's rules wrapped around its hooks.
func (e *entry[T]) importDataset(r *Request, index int, dataset *importexport.Dataset, dryRun bool) (*importexport.Result, error) {
	resource, err := e.resource(r, index)
	if err != nil {
		return nil, err
	}
	permissions := r.Site.permissionsFor(r, e)
	scope := resource.QuerySet
	resource.QuerySet = func(ctx context.Context) query.QuerySet[T] {
		visible := e.typedView(r)
		if scope != nil {
			visible = visible.And(scope(ctx))
		}
		return visible
	}
	transactional := func(ctx context.Context) *Request {
		return &Request{Request: r.Request.WithContext(ctx), User: r.User, Site: r.Site}
	}
	beforeSave, afterSave := resource.BeforeSaveInstance, resource.AfterSaveInstance
	resource.BeforeSaveInstance = func(ctx context.Context, object *T, row importexport.Row, isNew bool) error {
		request := transactional(ctx)
		if err := e.checkImportAllowed(request, permissions, object, isNew); err != nil {
			return err
		}
		if beforeSave != nil {
			if err := beforeSave(ctx, object, row, isNew); err != nil {
				return err
			}
		}
		return e.beforeSave(request, object, isNew)
	}
	resource.AfterSaveInstance = func(ctx context.Context, object *T, row importexport.Row, isNew bool) error {
		if afterSave != nil {
			if err := afterSave(ctx, object, row, isNew); err != nil {
				return err
			}
		}
		return e.afterSave(transactional(ctx), object, isNew)
	}
	ctx := context.WithValue(r.Context(), requestKey{}, r)
	result, err := resource.Import(ctx, dataset, importexport.ImportOptions{DryRun: dryRun})
	if result != nil {
		for index := range result.Rows {
			if row := &result.Rows[index]; row.Error != nil {
				if message, ok := r.Site.friendlyError(row.Error); ok {
					row.Error = errors.New(message)
				}
			}
		}
	}
	return result, err
}

func (e *entry[T]) checkImportAllowed(r *Request, permissions Permissions, object *T, isNew bool) error {
	info := e.model
	var original any
	if isNew {
		if !permissions.Add {
			return fmt.Errorf("You don't have permission to add %s.", strings.ToLower(info.VerboseNamePlural))
		}
	} else {
		id := info.PKOf(object)
		allowed := map[int64]bool{}
		if permissions.Change {
			var err error
			if allowed, err = allowedSet(r.Context(), e.changeQuerySet(r), []int64{id}); err != nil {
				return err
			}
		}
		if !allowed[id] {
			return fmt.Errorf("You don't have permission to change this %s.", strings.ToLower(info.VerboseName))
		}
		var err error
		if original, err = query.ForModel(info).GetByPK(r.Context(), id); err != nil {
			return err
		}
	}
	for _, field := range info.Fields {
		if field.Kind != models.KindForeignKey {
			continue
		}
		id, _ := field.Get(object).(int64)
		if id == 0 || (original != nil && field.Get(original) == field.Get(object)) {
			continue
		}
		if !r.Site.relatedVisible(r, field, id) {
			return fmt.Errorf("%s: select a valid choice; %s #%d is not one of the available choices.", field.VerboseName, strings.ToLower(field.Related().VerboseName), id)
		}
	}
	return nil
}

// ---- export ----

// exportLink is one export choice; the list page adds its current query string in the browser.
type exportLink struct {
	Group, Label, Format string
	Resource             int
}

func exportLinks(transfer transferPolicy) []exportLink {
	var links []exportLink
	for _, resource := range transfer.Resources {
		if resource.Error != "" {
			continue
		}
		group := ""
		if len(transfer.Resources) > 1 {
			group = resource.Name
		}
		for _, format := range transfer.Formats {
			links = append(links, exportLink{Group: group, Label: strings.ToUpper(format.Name()), Format: format.Name(), Resource: resource.Index})
		}
	}
	return links
}

func (s *Site) exportView(w http.ResponseWriter, r *Request, entry modelEntry) {
	info := entry.info()
	transfer := entry.transfer(r)
	selected := r.Method == http.MethodPost
	if (selected && !transfer.ExportSelected) || (!selected && !transfer.Export) {
		s.renderError(w, r, http.StatusForbidden, "You can't export "+strings.ToLower(info.VerboseNamePlural)+".")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Invalid form.")
		return
	}
	format := transfer.format(r.Form.Get("format"))
	if format == nil {
		format = transfer.Formats[0]
	}
	resource, _ := strconv.Atoi(r.Form.Get("resource"))
	queryset := s.listQuerySet(r, entry, r.URL.Query())
	if selected {
		var ids []int64
		for _, raw := range r.PostForm["ids"] {
			if id, err := strconv.ParseInt(raw, 10, 64); err == nil && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			s.addFlash(w, r.Request, "info", "No items were selected.")
			redirect(w, r.Request, s.modelURL(info)+"?"+r.URL.RawQuery)
			return
		}
		queryset = queryset.Filter(query.FieldLookup(info.PK, "in", idValues(ids)...))
	}
	dataset, err := entry.exportDataset(r, resource, queryset)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var buffer bytes.Buffer
	if err := format.Write(&buffer, dataset); err != nil {
		s.serverError(w, r, err)
		return
	}
	filename := fmt.Sprintf("%s-%s.%s", info.Key(), time.Now().Format("2006-01-02"), format.Name())
	w.Header().Set("Content-Type", format.ContentType()+"; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = buffer.WriteTo(w)
}

// ---- import ----

type importPage struct {
	Meta       modelMeta
	Transfer   transferPolicy
	Action     string
	Accept     string
	Resource   int
	Error      string
	Preview    *importPreview
	Extensions string
}

type importPreview struct {
	Filename   string
	Data       string
	Headers    []string
	Rows       []importRow
	Totals     importTotals
	TotalRows  int
	Hidden     int
	OnlyIssues bool
	CanConfirm bool
}

type importTotals struct{ New, Update, Skip, Error, Invalid int }

type importRow struct {
	Number      int
	Type        string
	Object, URL string
	Cells       []importCell
	Errors      []string
}

type importCell struct{ Text, Error string }

const maxPreviewRows = 500

func (s *Site) importView(w http.ResponseWriter, r *Request, entry modelEntry) {
	info := entry.info()
	transfer := entry.transfer(r)
	if !transfer.Import {
		s.renderError(w, r, http.StatusForbidden, "You can't import "+strings.ToLower(info.VerboseNamePlural)+".")
		return
	}
	page := importPage{Meta: s.meta(r, entry), Transfer: transfer, Action: s.modelURL(info) + "import/", Accept: transfer.extensions()}
	page.Extensions = strings.ReplaceAll(page.Accept, ",", ", ")
	render := func(status int) {
		title := "Import " + strings.ToLower(info.VerboseNamePlural)
		s.render(w, "import.html", "base", status, s.newPage(w, r, title, info.Label(), s.modelCrumbs(entry, crumb{Label: "Import"}), page))
	}
	if r.Method != http.MethodPost {
		render(http.StatusOK)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize+1<<20)
	if err := r.ParseMultipartForm(maxImportSize); err != nil {
		page.Error = fmt.Sprintf("The upload failed; files can be at most %d MB.", maxImportSize>>20)
		render(http.StatusBadRequest)
		return
	}
	page.Resource, _ = strconv.Atoi(r.PostForm.Get("resource"))
	confirm := r.PostForm.Get("_confirm") == "1"
	var content []byte
	var filename string
	if confirm {
		decoded, err := base64.StdEncoding.DecodeString(r.PostForm.Get("data"))
		if err != nil {
			page.Error = "The confirmed file was damaged; upload it again."
			render(http.StatusBadRequest)
			return
		}
		content, filename = decoded, r.PostForm.Get("filename")
	} else {
		file, header, err := r.FormFile("file")
		if err != nil {
			page.Error = "Choose a file to import."
			render(http.StatusUnprocessableEntity)
			return
		}
		defer file.Close()
		if content, err = io.ReadAll(file); err != nil {
			page.Error = "The file could not be read."
			render(http.StatusBadRequest)
			return
		}
		filename = header.Filename
	}
	format := transfer.formatForFile(filename)
	if format == nil {
		page.Error = "Unsupported file type. Upload a " + page.Extensions + " file."
		render(http.StatusUnprocessableEntity)
		return
	}
	dataset, err := format.Read(bytes.NewReader(content))
	if err == nil && len(dataset.Rows) == 0 {
		err = errors.New("the file has no rows")
	}
	if err != nil {
		page.Error = "The file could not be read: " + strings.TrimPrefix(err.Error(), "importexport: ")
		render(http.StatusUnprocessableEntity)
		return
	}
	result, err := entry.importDataset(r, page.Resource, dataset, !confirm)
	if err != nil {
		page.Error = strings.TrimPrefix(err.Error(), "importexport: ")
		render(http.StatusUnprocessableEntity)
		return
	}
	if result.Committed {
		totals := result.Totals
		s.addFlash(w, r.Request, "success", fmt.Sprintf("Import finished: %d new, %d updated, %d skipped.",
			totals[importexport.RowNew], totals[importexport.RowUpdate], totals[importexport.RowSkip]))
		redirect(w, r.Request, s.modelURL(info))
		return
	}
	page.Preview = s.buildPreview(info, dataset, result, confirm)
	page.Preview.Filename = filename
	page.Preview.Data = base64.StdEncoding.EncodeToString(content)
	status := http.StatusOK
	if !page.Preview.CanConfirm {
		status = http.StatusUnprocessableEntity
	}
	render(status)
}

func (s *Site) buildPreview(info *models.ModelInfo, dataset *importexport.Dataset, result *importexport.Result, confirmed bool) *importPreview {
	totals := result.Totals
	preview := &importPreview{
		Headers:   dataset.Headers,
		TotalRows: len(result.Rows),
		Totals: importTotals{New: totals[importexport.RowNew], Update: totals[importexport.RowUpdate], Skip: totals[importexport.RowSkip],
			Error: totals[importexport.RowError], Invalid: totals[importexport.RowInvalid]},
	}
	preview.OnlyIssues = result.HasErrors() || result.HasValidationErrors()
	preview.CanConfirm = !confirmed && !preview.OnlyIssues
	for _, rowResult := range result.Rows {
		hasIssue := rowResult.Type == importexport.RowError || rowResult.Type == importexport.RowInvalid
		if preview.OnlyIssues && !hasIssue {
			continue
		}
		if len(preview.Rows) >= maxPreviewRows {
			preview.Hidden++
			continue
		}
		row := importRow{Number: rowResult.Number, Type: string(rowResult.Type), Object: rowResult.ObjectRepr}
		if rowResult.ObjectID != 0 && (rowResult.Type == importexport.RowUpdate || rowResult.Type == importexport.RowSkip) {
			row.URL = s.objectURL(info, rowResult.ObjectID)
		}
		cells := dataset.Row(rowResult.Number - 1)
		for _, header := range dataset.Headers {
			row.Cells = append(row.Cells, importCell{Text: cells[header], Error: rowResult.ValidationErrors[header]})
		}
		for header, message := range rowResult.ValidationErrors {
			if _, known := cells[header]; !known {
				row.Errors = append(row.Errors, message)
			}
		}
		slices.Sort(row.Errors)
		if rowResult.Error != nil {
			row.Errors = append(row.Errors, rowResult.Error.Error())
		}
		preview.Rows = append(preview.Rows, row)
	}
	return preview
}
