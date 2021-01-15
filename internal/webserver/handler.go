package webserver

import (
	"html/template"

	"shiori/internal/database"
	"github.com/go-shiori/warc"
	cch "github.com/patrickmn/go-cache"
)

var developmentMode = false

// Handler is handler for serving the web interface.
type handler struct {
	DB           database.DB
	DataDir      string
	RootPath     string
	UserCache    *cch.Cache
	ArchiveCache *cch.Cache

	templates map[string]*template.Template
}

func (h *handler) prepareArchiveCache() {
	h.ArchiveCache.OnEvicted(func(key string, data interface{}) {
		archive := data.(*warc.Archive)
		archive.Close()
	})
}

func (h *handler) prepareTemplates() error {
	// Prepare variables
	var err error
	h.templates = make(map[string]*template.Template)

	// Prepare func map
	funcMap := template.FuncMap{
		"html": func(s string) template.HTML {
			return template.HTML(s)
		},
	}

	// Create template for index and content
	for _, name := range []string{"index", "content"} {
		h.templates[name], err = createTemplate(name+".html", funcMap)
		if err != nil {
			return err
		}
	}

	// Create template for archive overlay
	h.templates["archive"], err = template.New("archive").Delims("$$", "$$").Parse(
		`<div id="shiori-archive-header">
		<a href="$$.URL$$" target="_blank">View Original</a>
		$$if .HasContent$$
		<a href="/bookmark/$$.ID$$/content">View Readable</a>
		$$end$$
		</div>`)
	if err != nil {
		return err
	}

	return nil
}
