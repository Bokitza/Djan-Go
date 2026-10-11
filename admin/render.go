package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/yoav/djan-go/auth"
)

var pages = []string{"login.html", "dashboard.html", "list.html", "form.html", "error.html", "import.html"}

const flashCookie = "djan_flash"

type navModel struct {
	Label, URL, AddURL, Icon string
	Active                   bool
}

type navGroup struct {
	Label  string
	Models []navModel
}

type crumb struct{ Label, URL string }

type flash struct{ Level, Message string }

type paletteItem struct {
	Label  string `json:"label"`
	Hint   string `json:"hint"`
	URL    string `json:"url,omitempty"`
	Action string `json:"action,omitempty"`
	Icon   string `json:"-"`
	SVG    string `json:"svg"`
}

type pageData struct {
	SiteTitle   string
	Language    string
	Direction   string
	Prefix      string
	User        *auth.User
	Nav         []navGroup
	Title       string
	Breadcrumbs []crumb
	Flashes     []flash
	Palette     []paletteItem
	Page        any
}

func (s *Site) loadTemplates() (map[string]*template.Template, error) {
	functions := template.FuncMap{
		"json": func(value any) (string, error) {
			encoded, err := json.Marshal(value)
			return string(encoded), err
		},
		"initials": func(user *auth.User) string {
			name := strings.TrimSpace(user.FirstName + " " + user.LastName)
			if name == "" {
				name = user.Username
			}
			var letters []rune
			for _, word := range strings.Fields(name) {
				letters = append(letters, []rune(strings.ToUpper(word))[0])
				if len(letters) == 2 {
					break
				}
			}
			return string(letters)
		},
		"add":  func(a, b int) int { return a + b },
		"icon": icon,
		"dict": func(pairs ...any) map[string]any {
			values := map[string]any{}
			for index := 0; index+1 < len(pairs); index += 2 {
				values[fmt.Sprint(pairs[index])] = pairs[index+1]
			}
			return values
		},
		"t":  s.translate,
		"tf": s.translatef,
	}
	templates := map[string]*template.Template{}
	for _, page := range pages {
		parsed, err := template.New(page).Funcs(functions).ParseFS(templateFiles,
			"templates/base.html", "templates/partials.html", "templates/"+page)
		if err != nil {
			return nil, err
		}
		templates[page] = parsed
	}
	return templates, nil
}

func (s *Site) navigation(r *Request, active string) ([]navGroup, []paletteItem) {
	var groups []navGroup
	palette := []paletteItem{{Label: s.translate("Dashboard"), Hint: s.translate("Go to"), URL: s.Prefix + "/", Icon: "home"}}
	for _, entry := range s.sortedEntries() {
		info := entry.info()
		permissions := s.permissionsFor(r, entry)
		if !permissions.View {
			continue
		}
		if len(groups) == 0 || groups[len(groups)-1].Label != s.appLabel(info.App) {
			groups = append(groups, navGroup{Label: s.appLabel(info.App)})
		}
		item := navModel{Label: s.modelNamePlural(info), URL: s.modelURL(info), Icon: entry.icon(), Active: active == info.Label()}
		if permissions.Add {
			item.AddURL = s.modelURL(info) + "add/"
			palette = append(palette, paletteItem{Label: s.addTitle(info), Hint: s.appLabel(info.App), URL: item.AddURL, Icon: "plus"})
		}
		palette = append(palette, paletteItem{Label: s.modelNamePlural(info), Hint: s.appLabel(info.App), URL: item.URL, Icon: entry.icon()})
		groups[len(groups)-1].Models = append(groups[len(groups)-1].Models, item)
	}
	palette = append(palette,
		paletteItem{Label: s.translate("Toggle dark mode"), Hint: s.translate("Appearance"), Action: "theme", Icon: "moon"},
		paletteItem{Label: s.translate("Log out"), Hint: s.translate("Account"), Action: "logout", Icon: "log-out"})
	for index := range palette {
		palette[index].SVG = string(icon(palette[index].Icon))
	}
	return groups, palette
}

func titleCase(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func (s *Site) newPage(w http.ResponseWriter, r *Request, title, active string, breadcrumbs []crumb, page any) pageData {
	data := pageData{SiteTitle: s.Title, Language: s.languageCode(), Direction: s.direction(), Prefix: s.Prefix, Title: title, Breadcrumbs: breadcrumbs, Page: page, Flashes: s.takeFlashes(w, r.Request)}
	if r.User != nil {
		data.User = r.User
		data.Nav, data.Palette = s.navigation(r, active)
	}
	return data
}

func (s *Site) render(w http.ResponseWriter, page, name string, status int, data pageData) {
	var buffer bytes.Buffer
	if err := s.templates[page].ExecuteTemplate(&buffer, name, data); err != nil {
		log.Printf("admin: rendering %s/%s: %v", page, name, err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buffer.WriteTo(w)
}

func (s *Site) renderError(w http.ResponseWriter, r *Request, status int, message string) {
	statusText := s.translate(http.StatusText(status))
	data := s.newPage(w, r, statusText, "", []crumb{{Label: statusText}}, map[string]any{"Status": status, "Message": message})
	s.render(w, "error.html", "base", status, data)
}

func (s *Site) serverError(w http.ResponseWriter, r *Request, err error) {
	log.Printf("admin: %s %s: %v", r.Method, r.URL.Path, err)
	s.renderError(w, r, http.StatusInternalServerError, s.translate("Something went wrong. The error was logged."))
}

func (s *Site) addFlash(w http.ResponseWriter, r *http.Request, level, message string) {
	flashes := s.readFlashes(r)
	flashes = append(flashes, flash{Level: level, Message: message})
	encoded, _ := json.Marshal(flashes)
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: base64.RawURLEncoding.EncodeToString(encoded), Path: s.Prefix + "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (s *Site) readFlashes(r *http.Request) []flash {
	cookie, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return nil
	}
	var flashes []flash
	_ = json.Unmarshal(decoded, &flashes)
	return flashes
}

func (s *Site) takeFlashes(w http.ResponseWriter, r *http.Request) []flash {
	flashes := s.readFlashes(r)
	if flashes != nil {
		http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: s.Prefix + "/", MaxAge: -1})
	}
	return flashes
}

// redirect works for normal and HTMX requests.
func redirect(w http.ResponseWriter, r *http.Request, location string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func urlQueryEscape(value string) string { return url.QueryEscape(value) }

// withQuery returns "?..." with key set to value (removed when empty), dropping the listed keys.
func withQuery(values url.Values, key, value string, drop ...string) string {
	copied := url.Values{}
	for name, items := range values {
		copied[name] = append([]string(nil), items...)
	}
	for _, name := range drop {
		copied.Del(name)
	}
	if key != "" {
		if value == "" {
			copied.Del(key)
		} else {
			copied.Set(key, value)
		}
	}
	if len(copied) == 0 {
		return "?"
	}
	return "?" + copied.Encode()
}
