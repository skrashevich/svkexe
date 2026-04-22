package dashboard

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/svkexe/platform/internal/ctxkeys"
	"github.com/svkexe/platform/internal/db"
	"github.com/svkexe/platform/internal/runtime"
	"github.com/svkexe/platform/internal/secrets"
	"github.com/svkexe/platform/internal/shelley"
	"github.com/svkexe/platform/ui"
)

// Dashboard holds dependencies for the web dashboard.
type Dashboard struct {
	db           *db.DB
	runtime      runtime.ContainerRuntime
	materializer *secrets.Materializer
	domain        string
	encKey        []byte
	shelleyLLMCfg *shelley.LLMProxyConfig
	templates     *template.Template
	funcMap       template.FuncMap
}

// NewDashboard creates a Dashboard and parses all HTML templates.
func NewDashboard(database *db.DB, rt runtime.ContainerRuntime, materializer *secrets.Materializer, domain string, encKey []byte, shelleyLLM *shelley.LLMProxyConfig) (*Dashboard, error) {
	funcMap := template.FuncMap{
		"formatTime": func(t time.Time) string {
			return t.Format("2006-01-02 15:04")
		},
		"statusClass": func(status string) string {
			switch strings.ToLower(status) {
			case "running":
				return "running"
			case "stopped":
				return "stopped"
			case "starting", "pending", "creating":
				return "starting"
			default:
				return "error"
			}
		},
		"maskKey": func(key string) string {
			if len(key) <= 8 {
				return "••••••••"
			}
			return key[:4] + strings.Repeat("•", len(key)-8) + key[len(key)-4:]
		},
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(ui.Templates, "templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Dashboard{
		db:            database,
		runtime:       rt,
		materializer:  materializer,
		domain:        domain,
		encKey:        encKey,
		shelleyLLMCfg: shelleyLLM,
		templates:     tmpl,
		funcMap:       funcMap,
	}, nil
}

// RegisterRoutes mounts all dashboard routes onto r.
func (d *Dashboard) RegisterRoutes(r chi.Router) {
	r.Get("/", d.redirectToVMs)
	r.Get("/vms", d.getVMs)
	r.Get("/vms/list", d.getVMList)
	r.Get("/vms/create", d.getVMCreate)
	r.Post("/vms", d.postCreateVM)
	r.Post("/vms/{id}/start", d.postStartVM)
	r.Post("/vms/{id}/stop", d.postStopVM)
	r.Delete("/vms/{id}", d.deleteVM)
	r.Get("/vms/{id}/shell", d.getShell)
	r.Get("/vms/{id}/ws", d.handleWS)
	r.Get("/keys", d.getKeys)
	r.Put("/keys/{provider}", d.putKey)
	r.Delete("/keys/{provider}", d.deleteKey)

	r.Get("/ssh-keys", d.getSSHKeys)
	r.Post("/ssh-keys", d.postSSHKey)
	r.Delete("/ssh-keys/{id}", d.deleteSSHKey)
}

// redirectToVMs handles GET /dashboard/
func (d *Dashboard) redirectToVMs(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/dashboard/vms", http.StatusFound)
}

// templateData is the base data passed to every template render.
type templateData struct {
	User       *db.User
	Domain     string
	Containers []*db.Container
	Container  *db.Container
}

func (d *Dashboard) newData(r *http.Request) templateData {
	user, _ := r.Context().Value(ctxkeys.User).(*db.User)
	return templateData{
		User:   user,
		Domain: d.domain,
	}
}

func (d *Dashboard) render(w http.ResponseWriter, tmplName string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := d.templates.ExecuteTemplate(w, tmplName, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// partialPatterns lists glob patterns for template files that only define
// named snippets (no "content" block). renderPage includes all of them so
// that pages can {{template "vm_list_content" .}} etc.
var partialPatterns = []string{
	"templates/vm_list.html",
	"templates/vm_row.html",
	"templates/vm_create.html",
	"templates/key_row.html",
}

// renderPage renders a full page by parsing layout + the named page file
// together with all partials, so that each page's {{define "content"}} is
// isolated (no cross-page collision) but partial templates referenced by the
// page are available.
func (d *Dashboard) renderPage(w http.ResponseWriter, pageFile string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	files := append([]string{"templates/layout.html", "templates/" + pageFile}, partialPatterns...)
	tmpl, err := template.New("").Funcs(d.funcMap).ParseFS(ui.Templates, files...)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
	}
}
