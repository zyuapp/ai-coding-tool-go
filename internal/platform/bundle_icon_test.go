package platform

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// png is a PNG that is only a header: an IHDR wide enough to sort by, which is all the reader looks at.
func png(width uint32) []byte {
	data := make([]byte, 25)
	copy(data, pngMagic)
	copy(data[12:], "IHDR")
	binary.BigEndian.PutUint32(data[16:], width)
	return data
}

type icnsRecord struct {
	kind    string
	payload []byte
}

func icns(records ...icnsRecord) []byte {
	var body bytes.Buffer
	for _, record := range records {
		header := make([]byte, 8)
		copy(header, record.kind)
		binary.BigEndian.PutUint32(header[4:], uint32(len(record.payload)+8))
		body.Write(header)
		body.Write(record.payload)
	}
	head := make([]byte, 8)
	copy(head, "icns")
	binary.BigEndian.PutUint32(head[4:], uint32(body.Len()+8))
	return append(head, body.Bytes()...)
}

func TestTheReaderTakesTheSmallestIconAMenuRowCanUse(t *testing.T) {
	if got := pngWidth(PNGInICNS(icns(icnsRecord{"ic13", png(256)}, icnsRecord{"ic11", png(32)}, icnsRecord{"ic07", png(128)}))); got != 128 {
		t.Fatalf("got %d", got)
	}
	if got := pngWidth(PNGInICNS(icns(icnsRecord{"icp4", png(16)}, icnsRecord{"icp5", png(24)}))); got != 24 {
		t.Fatalf("nothing big enough: got %d", got)
	}
	if PNGInICNS(icns(icnsRecord{"ic04", bytes.Repeat([]byte{7}, 64)}, icnsRecord{"TOC ", bytes.Repeat([]byte{1}, 32)})) != nil {
		t.Fatal("a packed format was taken")
	}
	if PNGInICNS([]byte("not an icns at all")) != nil || PNGInICNS(make([]byte, 4)) != nil {
		t.Fatal("not an icns")
	}
}

func bundle(t *testing.T, plist string, icons map[string][]byte) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Example.app")
	resources := filepath.Join(app, "Contents", "Resources")
	os.MkdirAll(resources, 0o755)
	os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644)
	for name, data := range icons {
		os.WriteFile(filepath.Join(resources, name), data, 0o644)
	}
	return app
}

func plistNaming(icon string) string {
	return `<?xml version="1.0"?>` + "\n<plist><dict><key>CFBundleIconFile</key><string>" + icon + "</string></dict></plist>"
}

func iconWidth(t *testing.T, url *string) uint32 {
	t.Helper()
	if url == nil || !strings.HasPrefix(*url, "data:image/png;base64,") {
		t.Fatalf("got %v", url)
	}
	data, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(*url, "data:image/png;base64,"))
	return pngWidth(data)
}

func TestABundleIsReadAtItsWord(t *testing.T) {
	app := bundle(t, plistNaming("Example.icns"), map[string][]byte{"Example.icns": icns(icnsRecord{"ic11", png(32)}), "other.icns": icns(icnsRecord{"ic11", png(64)})})
	if iconWidth(t, BundleIcon(app)) != 32 {
		t.Fatal("the named icon lost to the other one")
	}
	iconWidth(t, BundleIcon(bundle(t, plistNaming("Example"), map[string][]byte{"Example.icns": icns(icnsRecord{"ic11", png(32)})})))
}

func TestABundleThatNamesNoneGivesUpItsOnlyIcon(t *testing.T) {
	iconWidth(t, BundleIcon(bundle(t, "<plist><dict/></plist>", map[string][]byte{"Example.icns": icns(icnsRecord{"ic11", png(32)})})))
	many := bundle(t, "<plist><dict/></plist>", map[string][]byte{"a.icns": icns(icnsRecord{"ic11", png(32)}), "b.icns": icns(icnsRecord{"ic11", png(32)})})
	if BundleIcon(many) != nil {
		t.Fatal("guessed between several")
	}
	if BundleIcon(bundle(t, plistNaming("Missing.icns"), nil)) != nil || BundleIcon(bundle(t, plistNaming("Example.icns"), map[string][]byte{"Example.icns": []byte("rubbish")})) != nil || BundleIcon("/no/such/bundle.app") != nil {
		t.Fatal("an unreadable icon was read")
	}
}
