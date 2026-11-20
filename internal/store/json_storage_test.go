package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJSONStorageKeepsOrderAndAdoptsOnlyMissingKeys(t *testing.T) {
	file := filepath.Join(t.TempDir(), "profile", WindowStorageFile)
	stored := `{"b":"1","a":"two \"quoted\"\nline","skip":3,"a":"last"}`
	os.MkdirAll(filepath.Dir(file), 0o755)
	os.WriteFile(file, []byte(stored), 0o644)
	storage := NewJSONStorage(file)
	if value, ok := storage.GetItem("a"); !ok || value != "last" {
		t.Fatalf("a = %q", value)
	}
	if _, ok := storage.GetItem("skip"); ok {
		t.Fatal("a value that is not a string is not kept")
	}
	changed, err := storage.Adopt(map[string]string{"a": "window", "c": "<&> \x01"})
	if err != nil || !changed {
		t.Fatalf("adopt %v %v", changed, err)
	}
	data, _ := os.ReadFile(file)
	if want := `{"b":"1","a":"last","c":"<&>` + " " + `\u0001"}`; string(data) != want {
		t.Fatalf("file %s", data)
	}
	if changed, _ := storage.Adopt(map[string]string{"c": "again"}); changed {
		t.Fatal("adopting held keys changes nothing")
	}
	os.WriteFile(file, []byte("{not json"), 0o644)
	if _, ok := NewJSONStorage(file).GetItem("b"); ok {
		t.Fatal("an unreadable file reads as empty")
	}
}

func TestJSONStorageRewritesARealWindowFileUnchanged(t *testing.T) {
	source := os.Getenv("ACT_WINDOW_FIXTURE")
	if source == "" {
		t.Skip("ACT_WINDOW_FIXTURE names a copy of window.v1.json")
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), WindowStorageFile)
	os.WriteFile(file, original, 0o644)
	storage := NewJSONStorage(file)
	key := "aicodingtool.view-preferences.v1"
	value, _ := storage.GetItem(key)
	if err := storage.SetItem(key, value); err != nil {
		t.Fatal(err)
	}
	if written, _ := os.ReadFile(file); string(written) != string(original) {
		t.Fatalf("rewritten differently:\n%s\n%s", original, written)
	}
}
