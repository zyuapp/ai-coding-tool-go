package platform

import (
	"path/filepath"
	"testing"
)

func TestInstalledAppsAreThoseFoundWhereTheCatalogSays(t *testing.T) {
	folder := t.TempDir()
	writeExecutable(t, filepath.Join(folder, "act-editor"), "#!/bin/sh\n")
	holdSearchPath(t, folder)
	missing := filepath.Join(folder, "Missing.app")
	opener := NewAppOpener(AppCatalog{
		Apps: []ExternalApp{{ID: "editor", Label: "Editor", Kind: "editor"}, {ID: "gone", Label: "Gone", Kind: "editor"}, {ID: "files", Label: "Files", Kind: "files"}},
		Candidates: func(id, home, target string) []AppCandidate {
			switch id {
			case "editor":
				return []AppCandidate{{ID: id, Command: "act-editor", Args: []string{target}}}
			case "gone":
				return []AppCandidate{{ID: id, Command: "open", Probe: &missing}, {ID: id, Command: "act-gone"}}
			case "files":
				return []AppCandidate{{ID: id, Command: "open", Probe: &folder}}
			}
			return nil
		},
	})
	apps := opener.ListInstalled()
	if len(apps) != 2 || apps[0].ID != "editor" || apps[1].ID != "files" {
		t.Fatalf("got %#v", apps)
	}
	if err := opener.OpenFolder("editor", folder); err != nil {
		t.Fatal(err)
	}
	if err := opener.OpenFolder("gone", folder); err == nil || err.Error() != "Gone is not installed on this machine." {
		t.Fatalf("got %v", err)
	}
	if err := opener.OpenFolder("unknown", folder); err == nil || err.Error() != "AI Coding Tool does not know that application." {
		t.Fatalf("got %v", err)
	}
}
