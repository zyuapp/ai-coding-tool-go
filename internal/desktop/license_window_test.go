package desktop

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLicenseReaderServesOnlyShippedDocuments(t *testing.T) {
	root := t.TempDir()
	for _, packaged := range []bool{false, true} {
		directory := filepath.Join(root, "assets", "legal")
		if packaged {
			directory = filepath.Join(root, "legal")
		}
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		overview := "MIT License\nCopyright <Example> & \"Contributors\"\n"
		write := func(name, text string) {
			if err := os.WriteFile(filepath.Join(directory, name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("THIRD-PARTY-NOTICES.txt", overview)
		write("DEPENDENCIES.html", "<html><body><h1>Dependency licenses</h1></body></html>")
		write("RENDERER.md", "# Renderer\nMIT License\n")
		write("ignored.json", "{}")
		files, err := licenseFiles(packaged, root, root)
		if err != nil || len(files.names) != 3 {
			t.Fatalf("packaged=%v: files %v, %v", packaged, files, err)
		}

		page := licenseResponse(licenseRoot, files)
		body := string(page.body)
		if page.status != 200 || !strings.Contains(page.csp, "default-src 'none'") {
			t.Fatalf("overview: %d %q", page.status, page.csp)
		}
		if !strings.Contains(body, "<pre>"+escapeHTML(overview)+"</pre>") || len(regexp.MustCompile(`<a href=`).FindAllString(body, -1)) != 3 {
			t.Fatalf("overview body: %s", body)
		}
		if !strings.Contains(body, `<a href="`+licenseRoot+`THIRD-PARTY-NOTICES.txt" aria-current="page">Overview</a>`) {
			t.Fatalf("overview is not the current page: %s", body)
		}
		if html := string(licenseResponse(licenseRoot+"DEPENDENCIES.html", files).body); !strings.Contains(html, `<iframe title="DEPENDENCIES.html" sandbox="allow-same-origin" src="`+licenseRoot+`raw/DEPENDENCIES.html">`) {
			t.Fatalf("html document is not framed: %s", html)
		}
		if raw := string(licenseResponse(licenseRoot+"raw/DEPENDENCIES.html", files).body); !strings.Contains(raw, "<h1>Dependency licenses</h1>") {
			t.Fatalf("raw html: %s", raw)
		}
		if md := string(licenseResponse(licenseRoot+"RENDERER.md", files).body); !strings.Contains(md, "# Renderer\nMIT License") {
			t.Fatalf("markdown: %s", md)
		}
		for _, address := range []string{licenseRoot + "ignored.json", licenseRoot + "..%2Fsecret.txt", "file:///etc/passwd", licenseScheme + "://elsewhere/THIRD-PARTY-NOTICES.txt"} {
			if status := licenseResponse(address, files).status; status != 404 {
				t.Fatalf("%s answered %d", address, status)
			}
		}
		if err := os.Remove(filepath.Join(directory, "THIRD-PARTY-NOTICES.txt")); err != nil {
			t.Fatal(err)
		}
		if status := licenseResponse(licenseRoot, files).status; status != 500 {
			t.Fatalf("a missing document answered %d", status)
		}
	}
}
