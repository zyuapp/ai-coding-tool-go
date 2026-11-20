package desktop

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	licenseScheme   = "aicodingtool-licenses"
	licenseRoot     = licenseScheme + "://notices/"
	licenseOverview = "THIRD-PARTY-NOTICES.txt"
	licenseCSP      = "default-src 'none'; style-src 'unsafe-inline'; frame-src " + licenseScheme + ":; img-src data:"
)

var licenseDocument = regexp.MustCompile(`(?i)\.(txt|md|html)$`)

// licenseDocuments are the shipped notices in the order the reader lists them. Only these are
// addressable; a URL never becomes a filesystem path.
type licenseDocuments struct {
	names []string
	paths map[string]string
}

func (d *licenseDocuments) add(name, path string) {
	if _, ok := d.paths[name]; !ok {
		d.names = append(d.names, name)
	}
	d.paths[name] = path
}

func licenseFiles(packaged bool, resources, appPath string) (licenseDocuments, error) {
	root := filepath.Join(appPath, "assets", "legal")
	if packaged {
		root = filepath.Join(resources, "legal")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return licenseDocuments{}, err
	}
	files := licenseDocuments{paths: map[string]string{}}
	for _, entry := range entries {
		if licenseDocument.MatchString(entry.Name()) {
			files.add(entry.Name(), filepath.Join(root, entry.Name()))
		}
	}
	if !packaged {
		renderer := filepath.Join(appPath, "frontend", "dist", "legal", "RENDERER-THIRD-PARTY-LICENSES.md")
		if _, err := os.Stat(renderer); err == nil {
			files.add(filepath.Base(renderer), renderer)
		}
	}
	return files, nil
}

type licenseAnswer struct {
	status      int
	contentType string
	csp         string
	body        []byte
}

func escapeHTML(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(value)
}

// encodeURIComponent escapes as JavaScript's function of that name does.
func encodeURIComponent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		if 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || strings.IndexByte("-_.!~*'()", b) >= 0 {
			out.WriteByte(b)
		} else {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

func licenseResponse(address string, files licenseDocuments) licenseAnswer {
	notFound := licenseAnswer{status: 404, contentType: "text/plain;charset=UTF-8", body: []byte("License not found.")}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != licenseScheme || parsed.Host != "notices" {
		return notFound
	}
	path := parsed.EscapedPath()
	raw := strings.HasPrefix(path, "/raw/")
	if raw {
		path = path[5:]
	} else {
		path = strings.TrimPrefix(path, "/")
	}
	name, err := url.PathUnescape(path)
	if err != nil {
		return notFound
	}
	if name == "" {
		name = licenseOverview
	}
	file, ok := files.paths[name]
	if !ok {
		return notFound
	}
	answer := func(body string) licenseAnswer {
		return licenseAnswer{status: 200, contentType: "text/html; charset=utf-8", csp: licenseCSP, body: []byte(body)}
	}
	text, err := os.ReadFile(file)
	if err != nil {
		return licenseAnswer{status: 500, contentType: "text/plain;charset=UTF-8", body: []byte("This license document could not be read. Close this window and try again.")}
	}
	html := strings.HasSuffix(name, ".html")
	if raw {
		if html {
			return answer(string(text))
		}
		return answer(escapeHTML(string(text)))
	}
	var links strings.Builder
	for _, entry := range files.names {
		label := entry
		if entry == licenseOverview {
			label = "Overview"
		}
		current := ""
		if entry == name {
			current = ` aria-current="page"`
		}
		fmt.Fprintf(&links, `<a href="%s%s"%s>%s</a>`, licenseRoot, encodeURIComponent(entry), current, escapeHTML(label))
	}
	content := "<pre>" + escapeHTML(string(text)) + "</pre>"
	if html {
		content = fmt.Sprintf(`<iframe title="%s" sandbox="allow-same-origin" src="%sraw/%s"></iframe>`, escapeHTML(name), licenseRoot, encodeURIComponent(name))
	}
	return answer(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Open Source Licenses</title>
      <style>
        :root { color-scheme: light dark; font: 14px system-ui, sans-serif; }
        * { box-sizing: border-box; } body { margin: 0; display: flex; height: 100vh; }
        nav { width: 250px; flex-shrink: 0; overflow: auto; padding: 20px 12px; border-right: 1px solid GrayText; }
        h1 { font-size: 16px; margin: 0 8px 20px; } a { display: block; padding: 10px 8px; border-radius: 6px; color: inherit; text-decoration: none; overflow-wrap: anywhere; font-size: 12px; }
        a:hover, a[aria-current] { background: light-dark(#eee, #303030); } a:focus-visible { outline: 2px solid Highlight; }
        main { flex: 1; min-width: 0; overflow: auto; } pre { margin: 0; padding: 28px; white-space: pre-wrap; overflow-wrap: anywhere; font: 13px/1.65 ui-monospace, monospace; }
        iframe { width: 100%; height: 100%; border: 0; background: white; }
      </style></head><body><nav aria-label="License documents"><h1>Open Source Licenses</h1>` + links.String() + `</nav><main>` + content + `</main></body></html>`)
}

// licenses holds the documents the open viewer serves.
var licenses struct {
	sync.Mutex
	files licenseDocuments
}

func servedLicense(address string) licenseAnswer {
	licenses.Lock()
	files := licenses.files
	licenses.Unlock()
	return licenseResponse(address, files)
}

// openSourceLicenses keeps one reader, independent of the OS's text-editor associations.
func (app *App) openSourceLicenses(packaged bool, resources, appPath string) error {
	files, err := licenseFiles(packaged, resources, appPath)
	if err != nil {
		return err
	}
	licenses.Lock()
	licenses.files = files
	licenses.Unlock()
	return app.nativeOpenLicenses(files)
}
