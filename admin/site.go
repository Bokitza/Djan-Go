package admin

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/yoav-rogat/djan-go/auth"
	"github.com/yoav-rogat/djan-go/importexport"
	"github.com/yoav-rogat/djan-go/models"
	"github.com/yoav-rogat/djan-go/query"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

// Site is an admin site mounted under Prefix (default "/admin").
type Site struct {
	Title  string
	Prefix string
	// Language selects the UI translation ("he" also switches the layout to right-to-left).
	Language string
	// DateFormat and DateTimeFormat are Go time layouts for displayed values.
	DateFormat     string
	DateTimeFormat string

	messages  map[string]string
	appLabels map[string]string
	entries   []modelEntry
	byKey     map[string]modelEntry
	templates map[string]*template.Template
}

// NewSite creates an admin site. The auth User and Group models are registered automatically.
func NewSite(title string) *Site {
	site := &Site{Title: title, Prefix: "/admin", byKey: map[string]modelEntry{}}
	registerAuthAdmins(site)
	return site
}

// modelEntry is the type-erased view of a registered ModelAdmin used by the handlers.
type modelEntry interface {
	info() *models.ModelInfo
	config() *resolvedConfig
	icon() string
	viewQuerySet(r *Request) query.Dynamic
	changeQuerySet(r *Request) query.Dynamic
	deleteQuerySet(r *Request) query.Dynamic
	canAddRule(r *Request) bool
	permissionRule(r *Request, granted Permissions) Permissions
	beforeSave(r *Request, object any, isNew bool) error
	afterSave(r *Request, object any, isNew bool) error
	extraFields() []extraField
	transfer(r *Request) transferPolicy
	exportDataset(r *Request, resource int, queryset query.Dynamic) (*importexport.Dataset, error)
	importDataset(r *Request, resource int, dataset *importexport.Dataset, dryRun bool) (*importexport.Result, error)
}

type entry[T any] struct {
	model    *models.ModelInfo
	admin    ModelAdmin[T]
	resolved *resolvedConfig
}

func (e *entry[T]) info() *models.ModelInfo    { return e.model }
func (e *entry[T]) config() *resolvedConfig    { return e.resolved }
func (e *entry[T]) extraFields() []extraField  { return e.admin.extras }
func (e *entry[T]) canAddRule(r *Request) bool { return e.admin.CanAdd == nil || e.admin.CanAdd(r) }
func (e *entry[T]) permissionRule(r *Request, granted Permissions) Permissions {
	if e.admin.Permissions == nil {
		return granted
	}
	return e.admin.Permissions(r, granted)
}
func (e *entry[T]) base() query.QuerySet[T] { return query.Objects[T]() }
func (e *entry[T]) apply(rule RuleFunc[T], r *Request, qs query.QuerySet[T]) query.QuerySet[T] {
	if rule == nil {
		return qs
	}
	return rule(r, qs)
}

func (e *entry[T]) icon() string {
	if e.admin.Icon != "" {
		return e.admin.Icon
	}
	return "table"
}

func (e *entry[T]) typedView(r *Request) query.QuerySet[T] {
	return e.apply(e.admin.ViewQuerySet, r, e.base())
}

func (e *entry[T]) viewQuerySet(r *Request) query.Dynamic { return e.typedView(r).Dynamic() }

func (e *entry[T]) changeQuerySet(r *Request) query.Dynamic {
	return e.apply(e.admin.ChangeQuerySet, r, e.typedView(r)).Dynamic()
}

func (e *entry[T]) deleteQuerySet(r *Request) query.Dynamic {
	return e.apply(e.admin.DeleteQuerySet, r, e.typedView(r)).Dynamic()
}

func (e *entry[T]) beforeSave(r *Request, object any, isNew bool) error {
	if e.admin.BeforeSave == nil {
		return nil
	}
	return e.admin.BeforeSave(r, object.(*T), isNew)
}

func (e *entry[T]) afterSave(r *Request, object any, isNew bool) error {
	if e.admin.AfterSave == nil {
		return nil
	}
	return e.admin.AfterSave(r, object.(*T), isNew)
}

// Register adds model T to the site. It panics with a descriptive error when a field reference
// in Configure is invalid, so configuration mistakes surface at startup.
func Register[T any](site *Site, modelAdmin ModelAdmin[T]) {
	_, file, line, _ := runtime.Caller(1)
	location := fmt.Sprintf("%s:%d", file, line)
	info, ok := models.InfoFor[T]()
	if !ok {
		panic(fmt.Sprintf("admin: %s: model %T is not registered; call models.Register first", location, *new(T)))
	}
	config := &Config{}
	if modelAdmin.Configure != nil {
		modelAdmin.Configure((*T)(info.Probe()), config)
	}
	resolved, problems := resolveConfig(info, config)
	if policy := modelAdmin.ImportExport; policy != nil {
		if !policy.Import && !policy.Export && !policy.ExportSelected {
			problems = append(problems, errors.New("ImportExport: enable Import, Export or ExportSelected"))
		}
		for _, resource := range policy.Resources {
			if err := resource.Check(); err != nil {
				problems = append(problems, fmt.Errorf("ImportExport: %w", err))
			}
		}
	}
	if len(problems) > 0 {
		messages := make([]string, len(problems))
		for index, problem := range problems {
			messages[index] = "\n  " + problem.Error()
		}
		panic(fmt.Sprintf("admin: %s: invalid admin for %s:%s", location, info.Label(), strings.Join(messages, "")))
	}
	key := info.App + "/" + info.Key()
	if _, exists := site.byKey[key]; exists {
		panic(fmt.Sprintf("admin: %s: %s is already registered", location, info.Label()))
	}
	for _, field := range modelAdmin.ManyToMany {
		if info.FieldByName(field.editor.name()) != nil {
			panic(fmt.Sprintf("admin: %s: many-to-many %q clashes with a field of %s", location, field.editor.name(), info.Label()))
		}
		modelAdmin.extras = append(modelAdmin.extras, field.editor)
	}
	registered := &entry[T]{model: info, admin: modelAdmin, resolved: resolved}
	site.entries = append(site.entries, registered)
	site.byKey[key] = registered
}

// Unregister removes a model (for example the built-in auth admins) from the site.
func Unregister[T any](site *Site) {
	info, ok := models.InfoFor[T]()
	if !ok {
		return
	}
	delete(site.byKey, info.App+"/"+info.Key())
	site.entries = slices.DeleteFunc(site.entries, func(entry modelEntry) bool { return entry.info() == info })
}

// Check verifies that the models behind every registered admin are valid.
func (s *Site) Check() error {
	var problems []error
	for _, entry := range s.entries {
		for _, field := range entry.info().Fields {
			if field.Kind == models.KindForeignKey && field.Related() == nil {
				problems = append(problems, fmt.Errorf("admin: %s references unregistered model %s", field, field.RelatedType))
			}
		}
	}
	if _, err := s.loadTemplates(); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

func (s *Site) entryFor(info *models.ModelInfo) modelEntry {
	if info == nil {
		return nil
	}
	return s.byKey[info.App+"/"+info.Key()]
}

func (s *Site) sortedEntries() []modelEntry {
	sorted := slices.Clone(s.entries)
	sort.SliceStable(sorted, func(a, b int) bool {
		left, right := sorted[a].info(), sorted[b].info()
		if left.App != right.App {
			return left.App < right.App
		}
		return left.VerboseNamePlural < right.VerboseNamePlural
	})
	return sorted
}

// Permissions are what a request may do with a model in the admin.
type Permissions struct {
	View, Add, Change, Delete bool
}

func (s *Site) permissionsFor(r *Request, entry modelEntry) Permissions {
	ctx := r.Context()
	info := entry.info()
	change := r.User.HasModelPerm(ctx, info, "change")
	granted := Permissions{
		View:   change || r.User.HasModelPerm(ctx, info, "view"),
		Add:    r.User.HasModelPerm(ctx, info, "add"),
		Change: change,
		Delete: r.User.HasModelPerm(ctx, info, "delete"),
	}
	allowed := entry.permissionRule(r, granted)
	allowed.Add = allowed.Add && entry.canAddRule(r)
	return allowed
}

func (s *Site) modelURL(info *models.ModelInfo) string {
	return s.Prefix + "/" + info.App + "/" + info.Key() + "/"
}

func (s *Site) objectURL(info *models.ModelInfo, id int64) string {
	return fmt.Sprintf("%s%d/", s.modelURL(info), id)
}

func (s *Site) staticFS() fs.FS {
	sub, _ := fs.Sub(staticFiles, "static")
	return sub
}

// Handler returns the admin handler. Mount it at the site prefix:
//
//	mux.Handle("/admin/", site.Handler())
func (s *Site) Handler() http.Handler {
	s.Prefix = strings.TrimSuffix(s.Prefix, "/")
	templates, err := s.loadTemplates()
	if err != nil {
		panic(err)
	}
	s.templates = templates

	mux := http.NewServeMux()
	prefix := s.Prefix
	static := http.StripPrefix(prefix+"/static/", http.FileServerFS(s.staticFS()))
	mux.Handle("GET "+prefix+"/static/{file}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		static.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET "+prefix+"/login/{$}", s.loginPage)
	mux.HandleFunc("POST "+prefix+"/login/{$}", s.loginSubmit)
	mux.HandleFunc("POST "+prefix+"/logout/{$}", s.logout)

	staff := func(handler func(w http.ResponseWriter, r *Request)) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := auth.CurrentUser(r)
			if user == nil || !user.IsActive || !user.IsStaff {
				http.Redirect(w, r, prefix+"/login/?next="+urlQueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			handler(w, &Request{Request: r, User: user, Site: s})
		})
	}
	mux.Handle("GET "+prefix+"/{$}", staff(s.dashboard))
	mux.Handle("GET "+prefix+"/{app}/{model}/{$}", staff(s.withModel(s.listView)))
	mux.Handle("GET "+prefix+"/{app}/{model}/add/{$}", staff(s.withModel(s.addView)))
	mux.Handle("POST "+prefix+"/{app}/{model}/add/{$}", staff(s.withModel(s.addView)))
	mux.Handle("GET "+prefix+"/{app}/{model}/autocomplete/{$}", staff(s.withModel(s.autocomplete)))
	mux.Handle("POST "+prefix+"/{app}/{model}/bulk-delete/{$}", staff(s.withModel(s.bulkDelete)))
	mux.Handle("GET "+prefix+"/{app}/{model}/import/{$}", staff(s.withModel(s.importView)))
	mux.Handle("POST "+prefix+"/{app}/{model}/import/{$}", staff(s.withModel(s.importView)))
	mux.Handle("GET "+prefix+"/{app}/{model}/export/{$}", staff(s.withModel(s.exportView)))
	mux.Handle("POST "+prefix+"/{app}/{model}/export/{$}", staff(s.withModel(s.exportView)))
	mux.Handle("GET "+prefix+"/{app}/{model}/{id}/{$}", staff(s.withModel(s.changeView)))
	mux.Handle("POST "+prefix+"/{app}/{model}/{id}/{$}", staff(s.withModel(s.changeView)))
	mux.Handle("POST "+prefix+"/{app}/{model}/{id}/delete/{$}", staff(s.withModel(s.deleteView)))

	protection := http.NewCrossOriginProtection()
	return auth.Middleware(protection.Handler(mux))
}

func (s *Site) withModel(handler func(w http.ResponseWriter, r *Request, entry modelEntry)) func(w http.ResponseWriter, r *Request) {
	return func(w http.ResponseWriter, r *Request) {
		entry, ok := s.byKey[r.PathValue("app")+"/"+r.PathValue("model")]
		if !ok {
			s.renderError(w, r, http.StatusNotFound, s.translate("This model is not registered in the admin."))
			return
		}
		if !s.permissionsFor(r, entry).View {
			s.renderError(w, r, http.StatusForbidden, s.translatef("You don't have permission to view %s.", s.modelNamePlural(entry.info())))
			return
		}
		handler(w, r, entry)
	}
}
