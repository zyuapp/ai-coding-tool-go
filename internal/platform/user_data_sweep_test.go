package platform

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const day = 24 * time.Hour

func aged(t *testing.T, file string, age time.Duration, now time.Time) {
	t.Helper()
	os.WriteFile(file, []byte("png"), 0o644)
	os.Chtimes(file, now.Add(-age), now.Add(-age))
}

func TestOnlyOldUnnamedAttachmentsGoWithTheirSidecars(t *testing.T) {
	attachments := t.TempDir()
	now := time.Now()
	for _, name := range []string{"kept.png", "kept.png.context.json", "orphan.png", "orphan.png.context.json", "stray.png.context.json", "notes.txt"} {
		aged(t, filepath.Join(attachments, name), 30*day, now)
	}
	aged(t, filepath.Join(attachments, "fresh.png"), day, now)
	os.Mkdir(filepath.Join(attachments, "folder.png"), 0o755)

	swept, err := SweepOrphanAttachments(attachments, AttachmentNames([]string{"/elsewhere/kept.png"}), now, 7*day)
	if err != nil || swept != (SweepResult{Files: 3, Bytes: 9}) {
		t.Fatalf("got %#v %v", swept, err)
	}
	entries, _ := os.ReadDir(attachments)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, []string{"folder.png", "fresh.png", "kept.png", "kept.png.context.json", "notes.txt"}) {
		t.Fatalf("left %q", names)
	}
	if swept, err := SweepOrphanAttachments(filepath.Join(attachments, "missing"), nil, now, day); err != nil || swept.Files != 0 {
		t.Fatalf("missing directory: %v %v", swept, err)
	}
}

func TestTheRetiredCodexHomeIsHandedOverOnlyWhenItIsAFolder(t *testing.T) {
	userData := t.TempDir()
	var discarded []string
	discard := func(target string) error { discarded = append(discarded, target); return nil }
	if retired, _ := RetireLegacyCodexHome(userData, discard); retired {
		t.Fatal("retired nothing")
	}
	os.WriteFile(filepath.Join(userData, "codex"), []byte("a file"), 0o644)
	if retired, _ := RetireLegacyCodexHome(userData, discard); retired {
		t.Fatal("retired a file")
	}
	other := t.TempDir()
	os.Mkdir(filepath.Join(other, "codex"), 0o755)
	if retired, err := RetireLegacyCodexHome(other, discard); !retired || err != nil {
		t.Fatal("folder not retired")
	}
	if !slices.Equal(discarded, []string{filepath.Join(other, "codex")}) {
		t.Fatalf("discarded %q", discarded)
	}
	if _, err := RetireLegacyCodexHome(other, func(string) error { return errors.New("no trash") }); err == nil {
		t.Fatal("a failed discard was reported as done")
	}
}
