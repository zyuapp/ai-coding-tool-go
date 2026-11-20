package platform

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	icnsMagic  = "icns"
	icnsHeader = 8
	// wantedIconWidth is the smallest icon worth taking for a 20 point row, which a Retina screen draws at 40 pixels.
	wantedIconWidth = 40
)

var pngMagic = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

func isPNG(record []byte) bool {
	return len(record) > 24 && bytes.HasPrefix(record, pngMagic)
}

// pngWidth is the width a PNG gives in its IHDR chunk, which always comes first.
func pngWidth(record []byte) uint32 {
	return binary.BigEndian.Uint32(record[16:20])
}

// PNGInICNS is the PNG inside an `.icns` that suits a menu row: the smallest one at least 40 pixels
// across, else the widest there is. The older packed formats an `.icns` also holds are skipped.
func PNGInICNS(data []byte) []byte {
	if len(data) < icnsHeader || string(data[:4]) != icnsMagic {
		return nil
	}
	var best []byte
	offset := icnsHeader
	for offset+icnsHeader <= len(data) {
		length := int(binary.BigEndian.Uint32(data[offset+4 : offset+8]))
		if length < icnsHeader {
			break
		}
		end := min(offset+length, len(data))
		record := data[offset+icnsHeader : end]
		offset += length
		if !isPNG(record) {
			continue
		}
		if best == nil {
			best = record
			continue
		}
		width, chosen := pngWidth(record), pngWidth(best)
		better := width > chosen
		if chosen >= wantedIconWidth {
			better = width >= wantedIconWidth && width < chosen
		}
		if better {
			best = record
		}
	}
	return best
}

var bundleIconFile = regexp.MustCompile(`<key>CFBundleIconFile</key>\s*<string>([^<]+)</string>`)

// namedIcon is the icon a bundle names for itself. The extension is left off often enough to add it back.
func namedIcon(plist string) string {
	match := bundleIconFile.FindStringSubmatch(plist)
	if match == nil {
		return ""
	}
	named := strings.TrimSpace(match[1])
	if named == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(named), ".icns") {
		return named
	}
	return named + ".icns"
}

// iconPath is the icon file to read. A bundle that names one is taken at its word; one that does not,
// or whose property list is binary, gives up its only `.icns` when it has exactly one.
func iconPath(bundle string) string {
	resources := filepath.Join(bundle, "Contents", "Resources")
	plist, _ := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if named := namedIcon(string(plist)); named != "" {
		return filepath.Join(resources, named)
	}
	entries, _ := os.ReadDir(resources)
	var found []string
	for _, entry := range entries {
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".icns") {
			found = append(found, entry.Name())
		}
	}
	if len(found) == 1 {
		return filepath.Join(resources, found[0])
	}
	return ""
}

// BundleIcon is a macOS bundle's own icon as a data URL, or nil when it keeps one nothing here can read.
func BundleIcon(bundle string) *string {
	target := iconPath(bundle)
	if target == "" {
		return nil
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil
	}
	png := PNGInICNS(data)
	if png == nil {
		return nil
	}
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	return &url
}
