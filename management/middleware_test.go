package management_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yoav-rogat/djan-go/management"
)

func TestMiddlewareWrapsProjectRoutes(t *testing.T) {
	app := &management.App{
		Routes: func(mux *http.ServeMux) {
			mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(r.Header.Get("X-Wrapped")))
			})
		},
		Middleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Header.Set("X-Wrapped", "yes")
				next.ServeHTTP(w, r)
			})
		},
	}
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if recorder.Body.String() != "yes" {
		t.Fatalf("middleware did not run: %q", recorder.Body.String())
	}
}
