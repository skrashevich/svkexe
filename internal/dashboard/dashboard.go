package dashboard

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/aliases"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
	"github.com/skrashevich/svkexe/internal/updater"
	"github.com/skrashevich/svkexe/ui"
)

// Dashboard holds dependencies for the web dashboard.
type Dashboard struct {
	db             *db.DB
	runtime        runtime.ContainerRuntime
	materializer   *secrets.Materializer
	domain         string
	encKey         []byte
	picoclawLLMCfg *picoclaw.LLMProxyConfig
	updater        *updater.Service
	aliases        *aliases.Manager
	templates      *template.Template
	funcMap        template.FuncMap
}

// NewDashboard creates a Dashboard and parses all HTML templates.
// upd may be nil, in which case the system page reports that this deployment
// cannot update itself.
// aliasVerifier may be nil, in which case custom domains can be added but
// never verify, and therefore never route — the same trade-off NewServer
// documents for the REST API.
func NewDashboard(database *db.DB, rt runtime.ContainerRuntime, materializer *secrets.Materializer, domain string, encKey []byte, picoclawLLM *picoclaw.LLMProxyConfig, upd *updater.Service, aliasVerifier aliases.Verifier) (*Dashboard, error) {
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
			case "starting", "pending", "creating", "recreating":
				return "starting"
			default:
				return "error"
			}
		},
		"domain": func() string {
			return domain
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
		db:             database,
		runtime:        rt,
		materializer:   materializer,
		domain:         domain,
		encKey:         encKey,
		picoclawLLMCfg: picoclawLLM,
		updater:        upd,
		aliases:        aliases.New(database, rt, aliasVerifier, domain),
		templates:      tmpl,
		funcMap:        funcMap,
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
	r.Post("/vms/{id}/recreate", d.postRecreateVM)
	r.Post("/vms/{id}/publish", d.postPublish)
	r.Post("/vms/{id}/task/retry", d.postRetryTask)
	r.Post("/vms/{id}/aliases", d.postAddAlias)
	r.Post("/vms/{id}/aliases/{aliasID}/verify", d.postVerifyAlias)
	r.Delete("/vms/{id}/aliases/{aliasID}", d.deleteAlias)
	r.Delete("/vms/{id}", d.deleteVM)
	r.Get("/vms/{id}/shell", d.getShell)
	r.Get("/vms/{id}/ws", d.handleWS)
	r.Get("/keys", d.getKeys)
	// Static before the wildcard: "default" is a setting, not a provider.
	r.Post("/keys/default", d.postDefaultModel)
	r.Put("/keys/{provider}", d.putKey)
	r.Delete("/keys/{provider}", d.deleteKey)

	r.Get("/ssh-keys", d.getSSHKeys)
	r.Post("/ssh-keys", d.postSSHKey)
	r.Delete("/ssh-keys/{id}", d.deleteSSHKey)

	// Admin-only; the role is enforced inside the handlers because this router
	// sits behind the session middleware but not behind AdminMiddleware.
	r.Get("/system", d.getSystem)
	r.Get("/system/check", d.getUpdateCheck)
	r.Post("/system/update", d.postUpdate)
	r.Get("/system/status", d.getUpdateStatus)
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
	// IsAdmin gates admin-only chrome such as the System nav link. The layout
	// cannot derive it from User alone without hardcoding the role string in
	// the template.
	IsAdmin bool
}

func (d *Dashboard) newData(r *http.Request) templateData {
	user, _ := r.Context().Value(ctxkeys.User).(*db.User)
	return templateData{
		User:    user,
		Domain:  d.domain,
		IsAdmin: user != nil && user.Role == "admin",
	}
}

func (d *Dashboard) render(w http.ResponseWriter, tmplName string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := d.templates.ExecuteTemplate(w, tmplName, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// renderCard renders a single VM card with its custom domains attached. A
// failure to look up the aliases is logged, not returned: the VM itself is
// still there and its card must still render, just without a DNS section.
func (d *Dashboard) renderCard(w http.ResponseWriter, c *db.Container) {
	if err := d.db.AttachAliases(c); err != nil {
		log.Printf("render card for %s: attach aliases: %v", c.IncusName, err)
	}
	d.render(w, "vm_card", c)
}

// partialPatterns lists glob patterns for template files that only define
// named snippets (no "content" block). renderPage includes all of them so
// that pages can {{template "vm_list_content" .}} etc.
//
// templates/system.html is deliberately absent: it carries its own "content"
// block, and renderPage appends the partials after the page file, so listing it
// here would let the System page's content override every other page's. Its
// named fragments are rendered through d.render from the shared template set
// instead, the way vm_card is.
var partialPatterns = []string{
	"templates/vm_list.html",
	"templates/vm_row.html",
	"templates/vm_create.html",
	"templates/key_row.html",
	"templates/llm_body.html",
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
