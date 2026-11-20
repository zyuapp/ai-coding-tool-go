package updates

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func registration(t *testing.T, home string, associated *bool, writes *int) AppImageProtocolRegistration {
	t.Helper()
	icon := filepath.Join(home, "source.png")
	if err := os.WriteFile(icon, []byte("icon"), 0o644); err != nil {
		t.Fatal(err)
	}
	return AppImageProtocolRegistration{
		AppImage:     "/opt/AI Coding Tool.AppImage",
		Home:         home,
		IconSource:   icon,
		IsAssociated: func(context.Context, string) bool { return *associated },
		Associate: func(context.Context, string) error {
			*associated = true
			*writes++
			return nil
		},
	}
}

func TestAppImageRegistrationWritesOnceAndReplacesLinksWithoutWritingThroughThem(t *testing.T) {
	home := t.TempDir()
	associated, writes := false, 0
	options := registration(t, home, &associated, &writes)
	desktopPath := filepath.Join(home, ".local/share/applications", linuxDesktopFile)
	kept := filepath.Join(home, "kept")
	if err := os.WriteFile(kept, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(desktopPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(kept, desktopPath); err != nil {
		t.Fatal(err)
	}

	first, err := RegisterAppImageProtocol(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if contents, _ := os.ReadFile(kept); string(contents) != "keep" {
		t.Fatalf("wrote through the link: %q", contents)
	}
	if entry, _ := os.ReadFile(desktopPath); !strings.Contains(string(entry), `Exec="/opt/AI Coding Tool.AppImage" %U`) {
		t.Fatalf("entry %q", entry)
	}
	if !first.FilesChanged || !first.AssociationChanged {
		t.Fatalf("first %+v", first)
	}
	second, err := RegisterAppImageProtocol(context.Background(), options)
	if err != nil || second.FilesChanged || second.AssociationChanged || writes != 1 {
		t.Fatalf("second %+v, %d writes, %v", second, writes, err)
	}

	options.AppImage = "/home/me/new.AppImage"
	moved, err := RegisterAppImageProtocol(context.Background(), options)
	if entry, _ := os.ReadFile(desktopPath); err != nil || !moved.FilesChanged || !strings.Contains(string(entry), `Exec="/home/me/new.AppImage" %U`) {
		t.Fatalf("moved %+v %q %v", moved, entry, err)
	}
}

func TestAppImageRegistrationRefusesAnUnrelatedDesktopEntry(t *testing.T) {
	home := t.TempDir()
	associated, writes := false, 0
	options := registration(t, home, &associated, &writes)
	desktopPath := filepath.Join(home, ".local/share/applications", linuxDesktopFile)
	if err := os.MkdirAll(filepath.Dir(desktopPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktopPath, []byte("somebody else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterAppImageProtocol(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unrelated desktop entry") {
		t.Fatalf("err %v", err)
	}
	if contents, _ := os.ReadFile(desktopPath); string(contents) != "somebody else's" || writes != 0 {
		t.Fatalf("contents %q, %d writes", contents, writes)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/share/icons/hicolor/256x256/apps", linuxIconName+".png")); err == nil {
		t.Fatal("wrote the icon before refusing")
	}
}
