package browser

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// BrowserKind is a browser this machine may have. Roots are its user-data folders, every packaging
// of it included. Keychain names its macOS key; Libsecret and Kwallet name where Linux keeps it.
type BrowserKind struct {
	Key       string
	Name      string
	Family    CookieFamily
	Roots     []string
	Keychain  string
	Libsecret string
	Kwallet   string
}

// ResolvedSource is a profile found on disk, with the cookie file it reads from.
type ResolvedSource struct {
	domain.BrowserImportSource
	Kind BrowserKind
	File string
}

func catalog(goos, home, configHome string) []BrowserKind {
	switch goos {
	case "darwin":
		support := filepath.Join(home, "Library", "Application Support")
		chromium := func(key, name, folder string) BrowserKind {
			return BrowserKind{Key: key, Name: name, Family: FamilyChromium, Roots: []string{filepath.Join(support, folder)}, Keychain: name + " Safe Storage"}
		}
		return []BrowserKind{
			chromium("chrome", "Chrome", "Google/Chrome"),
			chromium("brave", "Brave", "BraveSoftware/Brave-Browser"),
			chromium("edge", "Microsoft Edge", "Microsoft Edge"),
			chromium("arc", "Arc", "Arc/User Data"),
			chromium("vivaldi", "Vivaldi", "Vivaldi"),
			chromium("opera", "Opera", "com.operasoftware.Opera"),
			chromium("chromium", "Chromium", "Chromium"),
			{Key: "firefox", Name: "Firefox", Family: FamilyFirefox, Roots: []string{filepath.Join(support, "Firefox")}},
		}
	case "linux":
		config := configHome
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		flatpak := func(app string, rest ...string) string {
			return filepath.Join(append([]string{home, ".var", "app", app}, rest...)...)
		}
		chromium := func(key, name string, roots []string, libsecret, kwallet string) BrowserKind {
			if kwallet == "" {
				kwallet = name
			}
			return BrowserKind{Key: key, Name: name, Family: FamilyChromium, Roots: roots, Libsecret: libsecret, Kwallet: kwallet}
		}
		return []BrowserKind{
			chromium("chrome", "Chrome", []string{filepath.Join(config, "google-chrome"), flatpak("com.google.Chrome", "config", "google-chrome")}, "chrome", ""),
			chromium("brave", "Brave", []string{
				filepath.Join(config, "BraveSoftware", "Brave-Browser"),
				flatpak("com.brave.Browser", "config", "BraveSoftware", "Brave-Browser"),
				filepath.Join(home, "snap", "brave", "current", ".config", "BraveSoftware", "Brave-Browser"),
			}, "brave", ""),
			// Edge and Opera keep their key under Chromium's name on Linux, and Vivaldi under Chrome's.
			chromium("edge", "Microsoft Edge", []string{filepath.Join(config, "microsoft-edge"), flatpak("com.microsoft.Edge", "config", "microsoft-edge")}, "chromium", "Chromium"),
			chromium("vivaldi", "Vivaldi", []string{filepath.Join(config, "vivaldi"), flatpak("com.vivaldi.Vivaldi", "config", "vivaldi")}, "chrome", "Chrome"),
			chromium("opera", "Opera", []string{filepath.Join(config, "opera")}, "chromium", "Chromium"),
			chromium("chromium", "Chromium", []string{
				filepath.Join(config, "chromium"),
				filepath.Join(home, "snap", "chromium", "common", "chromium"),
				flatpak("org.chromium.Chromium", "config", "chromium"),
			}, "chromium", ""),
			{Key: "firefox", Name: "Firefox", Family: FamilyFirefox, Roots: []string{
				filepath.Join(home, ".mozilla", "firefox"),
				filepath.Join(config, "mozilla", "firefox"),
				filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox"),
				flatpak("org.mozilla.firefox", ".mozilla", "firefox"),
				flatpak("org.mozilla.firefox", "config", "mozilla", "firefox"),
			}},
		}
	}
	return nil
}

func exists(file string) bool {
	_, err := os.Stat(file)
	return err == nil
}

func chromiumCookieFile(profile string) (string, bool) {
	for _, file := range []string{filepath.Join(profile, "Network", "Cookies"), filepath.Join(profile, "Cookies")} {
		if exists(file) {
			return file, true
		}
	}
	return "", false
}

func chromiumProfiles(kind BrowserKind, root string, rootIndex int) []*ResolvedSource {
	found := []*ResolvedSource{}
	add := func(folder, profile string) {
		if file, ok := chromiumCookieFile(filepath.Join(root, folder)); ok {
			found = append(found, &ResolvedSource{
				BrowserImportSource: domain.BrowserImportSource{ID: kind.Key + ":" + itoa(rootIndex) + ":" + folder, Browser: kind.Name, Profile: profile},
				Kind:                kind,
				File:                file,
			})
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "Local State")); err == nil {
		for _, entry := range localStateProfiles(data) {
			if strings.ContainsAny(entry.folder, `/\`) || strings.HasPrefix(entry.folder, ".") {
				continue
			}
			add(entry.folder, entry.name)
		}
	}
	// No list of profiles leaves the usual one, or the folder itself the way Opera keeps it.
	if len(found) == 0 {
		add("Default", "Default")
	}
	if len(found) == 0 {
		add(".", "Default")
	}
	return found
}

type profileEntry struct{ folder, name string }

// localStateProfiles reads `profile.info_cache` in the order Object.entries gives it: array-index
// keys ascending, then the rest in the order the file writes them.
func localStateProfiles(data []byte) []profileEntry {
	var state struct {
		Profile *struct {
			InfoCache jsontext.Value `json:"info_cache"`
		} `json:"profile"`
	}
	if jsonx.Unmarshal(data, &state) != nil || state.Profile == nil || state.Profile.InfoCache.Kind() != '{' {
		return nil
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(state.Profile.InfoCache))
	if _, err := decoder.ReadToken(); err != nil {
		return nil
	}
	entries := []profileEntry{}
	at := map[string]int{}
	for decoder.PeekKind() != '}' {
		key, err := decoder.ReadToken()
		if err != nil {
			return nil
		}
		folder := key.String()
		value, err := decoder.ReadValue()
		if err != nil {
			return nil
		}
		name := folder
		var info struct {
			Name any `json:"name"`
		}
		if value.Kind() == '{' && jsonx.Unmarshal(value, &info) == nil {
			if text, ok := info.Name.(string); ok && text != "" {
				name = text
			}
		}
		if index, ok := at[folder]; ok {
			entries[index].name = name
			continue
		}
		at[folder] = len(entries)
		entries = append(entries, profileEntry{folder: folder, name: name})
	}
	slices.SortStableFunc(entries, func(left, right profileEntry) int {
		leftIndex, leftOK := arrayIndex(left.folder)
		rightIndex, rightOK := arrayIndex(right.folder)
		switch {
		case leftOK && rightOK:
			return cmp.Compare(leftIndex, rightIndex)
		case leftOK:
			return -1
		case rightOK:
			return 1
		}
		return 0
	})
	return entries
}

func arrayIndex(key string) (uint64, bool) {
	value, err := strconv.ParseUint(key, 10, 32)
	if err != nil || value == math.MaxUint32 || strconv.FormatUint(value, 10) != key {
		return 0, false
	}
	return value, true
}

func itoa(value int) string { return strconv.Itoa(value) }

var (
	iniHeading = regexp.MustCompile(`^\s*\[(.+)\]\s*$`)
	iniPair    = regexp.MustCompile(`^\s*([^=]+?)\s*=\s*(.*?)\s*$`)
	lineBreak  = regexp.MustCompile(`\r?\n`)
)

// FirefoxProfileEntries is the [Profile…] sections of a Firefox profiles.ini, the default one first.
// Each section maps its keys to values, with its own name under "section".
func FirefoxProfileEntries(ini string) []map[string]string {
	sections := []map[string]string{}
	var current map[string]string
	for _, line := range lineBreak.Split(ini, -1) {
		if heading := iniHeading.FindStringSubmatch(line); heading != nil {
			current = map[string]string{"section": heading[1]}
			sections = append(sections, current)
		} else if current != nil {
			if pair := iniPair.FindStringSubmatch(line); pair != nil {
				current[pair[1]] = pair[2]
			}
		}
	}
	installed := map[string]bool{}
	for _, section := range sections {
		if strings.HasPrefix(section["section"], "Install") && section["Default"] != "" {
			installed[section["Default"]] = true
		}
	}
	profiles := []map[string]string{}
	for _, section := range sections {
		if strings.HasPrefix(section["section"], "Profile") && section["Path"] != "" {
			profiles = append(profiles, section)
		}
	}
	slices.SortStableFunc(profiles, func(left, right map[string]string) int {
		return boolNumber(installed[right["Path"]]) - boolNumber(installed[left["Path"]])
	})
	return profiles
}

func boolNumber(value bool) int {
	if value {
		return 1
	}
	return 0
}

func firefoxProfiles(kind BrowserKind, root string, rootIndex int) []*ResolvedSource {
	ini, err := os.ReadFile(filepath.Join(root, "profiles.ini"))
	if err != nil {
		return nil
	}
	found := []*ResolvedSource{}
	for _, entry := range FirefoxProfileEntries(string(ini)) {
		folder := entry["Path"]
		if entry["IsRelative"] != "0" {
			folder = filepath.Join(root, entry["Path"])
		}
		file := filepath.Join(folder, "cookies.sqlite")
		if !exists(file) {
			continue
		}
		profile := entry["Name"]
		if profile == "" {
			profile = filepath.Base(folder)
		}
		found = append(found, &ResolvedSource{
			BrowserImportSource: domain.BrowserImportSource{ID: kind.Key + ":" + itoa(rootIndex) + ":" + entry["Path"], Browser: kind.Name, Profile: profile},
			Kind:                kind,
			File:                file,
		})
	}
	return found
}

// Discover finds the browser profiles that hold cookies for goos, under home and the XDG config
// folder (empty for the default).
func Discover(goos, home, configHome string) []*ResolvedSource {
	found := []*ResolvedSource{}
	for _, kind := range catalog(goos, home, configHome) {
		for index, root := range kind.Roots {
			if !exists(root) {
				continue
			}
			if kind.Family == FamilyFirefox {
				found = append(found, firefoxProfiles(kind, root, index)...)
			} else {
				found = append(found, chromiumProfiles(kind, root, index)...)
			}
		}
	}
	return found
}

func discoverHere() []*ResolvedSource {
	home, _ := os.UserHomeDir()
	return Discover(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"))
}

// ListImportSources lists the browser profiles on this machine that hold cookies.
func ListImportSources(context.Context) ([]*domain.BrowserImportSource, error) {
	sources := []*domain.BrowserImportSource{}
	for _, source := range discoverHere() {
		item := source.BrowserImportSource
		sources = append(sources, &item)
	}
	return sources, nil
}

// resolve matches an id against what is on disk now, so nothing a caller sends becomes a path.
func resolve(sourceID string) (*ResolvedSource, error) {
	for _, source := range discoverHere() {
		if source.ID == sourceID {
			return source, nil
		}
	}
	return nil, errors.New("That browser profile is no longer on this computer.")
}

// BrowserDataBlocked is macOS refusing to let this app read another browser's data until it has
// Full Disk Access.
type BrowserDataBlocked struct{ Message string }

func (blocked *BrowserDataBlocked) Error() string { return blocked.Message }

// ReadProfile reads a profile's cookie database, naming what to allow when the system refuses.
func ReadProfile[T any](browser string, read func() (T, error), goos string) (T, error) {
	value, err := read()
	if err == nil {
		return value, nil
	}
	if goos != "darwin" || !(errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)) {
		return value, err
	}
	return value, &BrowserDataBlocked{Message: "macOS blocked reading " + browser + ". Turn on AI Coding Tool in Full Disk Access, then try again."}
}

// ListImportSites lists the sites a profile holds cookies for.
func ListImportSites(ctx context.Context, sourceID string) ([]*domain.BrowserImportSite, error) {
	source, err := resolve(sourceID)
	if err != nil {
		return nil, err
	}
	hosts, err := ReadProfile(source.Browser, func() ([]domain.BrowserCookieHost, error) {
		return ReadCookieHosts(ctx, source.Kind.Family, source.File, nowSeconds())
	}, runtime.GOOS)
	if err != nil {
		return nil, err
	}
	return domain.ImportSites(hosts), nil
}

// ExecStatus is how a helper command went.
type ExecStatus int

const (
	ExecOK ExecStatus = iota
	ExecFailed
	// ExecMissing is a command that is not installed.
	ExecMissing
)

// Exec runs a helper command and answers what it printed, without its last newline.
type Exec func(ctx context.Context, command string, args []string, timeout time.Duration) (string, ExecStatus)

func runHelper(ctx context.Context, command string, args []string, timeout time.Duration) (string, ExecStatus) {
	stdout, _, err := platform.Run(ctx, command, args, platform.RunOptions{Timeout: timeout})
	if err != nil {
		var failed *platform.RunError
		if errors.As(err, &failed) && failed.NotFound {
			return "", ExecMissing
		}
		if errors.Is(err, fs.ErrNotExist) {
			return "", ExecMissing
		}
		return "", ExecFailed
	}
	return strings.TrimSuffix(string(stdout), "\n"), ExecOK
}

// networkWallet is the KDE wallet Chromium stores its key in, which the user may have renamed from
// the default.
func networkWallet(ctx context.Context, exec Exec) string {
	for _, service := range []string{"kwalletd6", "kwalletd5"} {
		name, status := exec(ctx, "dbus-send", []string{"--session", "--print-reply=literal", "--dest=org.kde." + service, "/modules/" + service, "org.kde.KWallet.networkWallet"}, 5*time.Second)
		if status == ExecOK && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return "kdewallet"
}

// KeyHelpers notes the helper commands a key lookup found missing.
type KeyHelpers struct{ Missing []string }

// ChromiumKeysFor reads the password a Chromium browser encrypts cookies with from where goos keeps
// it. macOS asks the user before handing it over. Linux falls back to the fixed password Chromium
// uses without a keyring.
func ChromiumKeysFor(kind BrowserKind, helpers *KeyHelpers, goos string, exec Exec) ChromiumKeys {
	if exec == nil {
		exec = runHelper
	}
	return func(ctx context.Context, version string) ([][]byte, error) {
		if goos == "darwin" {
			password, status := exec(ctx, "security", []string{"find-generic-password", "-w", "-s", kind.Keychain}, 120*time.Second)
			if status != ExecOK {
				return nil, errors.New("macOS did not share " + kind.Name + "'s key. Choose Allow when it asks.")
			}
			return [][]byte{ChromiumKey(password, 1003)}, nil
		}
		if version == "v10" {
			return [][]byte{ChromiumKey("peanuts", 1)}, nil
		}
		wallet := networkWallet(ctx, exec)
		secret, secretStatus := exec(ctx, "secret-tool", []string{"lookup", "application", kind.Libsecret}, 30*time.Second)
		if secretStatus == ExecMissing {
			helpers.Missing = append(helpers.Missing, "secret-tool")
		}
		passwords := []string{}
		if secretStatus == ExecOK {
			passwords = append(passwords, secret)
		}
		if read, status := exec(ctx, "kwallet-query", []string{"--read-password", kind.Kwallet + " Safe Storage", "--folder", kind.Kwallet + " Keys", wallet}, 30*time.Second); status == ExecOK {
			passwords = append(passwords, read)
		}
		passwords = append(passwords, "")
		keys := [][]byte{}
		seen := map[string]bool{}
		for _, password := range passwords {
			if seen[password] {
				continue
			}
			seen[password] = true
			keys = append(keys, ChromiumKey(password, 1))
		}
		return keys, nil
	}
}

func readImportCookies(ctx context.Context, sourceID string, sites []string) ([]StoredCookie, int, error) {
	source, err := resolve(sourceID)
	if err != nil {
		return nil, 0, err
	}
	helpers := &KeyHelpers{}
	type read struct {
		cookies []StoredCookie
		skipped int
	}
	found, err := ReadProfile(source.Browser, func() (read, error) {
		cookies, skipped, err := ReadCookies(ctx, source.Kind.Family, source.File, sites, ChromiumKeysFor(source.Kind, helpers, runtime.GOOS, nil), nowSeconds())
		return read{cookies, skipped}, err
	}, runtime.GOOS)
	if err != nil {
		return nil, 0, err
	}
	if len(found.cookies) == 0 && found.skipped > 0 && slices.Contains(helpers.Missing, "secret-tool") {
		return nil, 0, errors.New(source.Browser + " keeps its key in the system keyring. Install secret-tool (libsecret-tools) and try again.")
	}
	return found.cookies, found.skipped, nil
}

// CookieDetails is what the panel's session needs to set one cookie the way the other browser held
// it, in the shape of Electron's CookiesSetDetails.
type CookieDetails struct {
	URL      string
	Name     string
	Value    string
	Domain   *string
	Path     string
	Secure   bool
	HTTPOnly bool
	SameSite SameSite
	// ExpirationDate is in Unix seconds, nil for a session cookie.
	ExpirationDate *float64
}

// CookieDetailsOf turns a stored cookie into the details to set it with.
func CookieDetailsOf(cookie StoredCookie) CookieDetails {
	host := strings.TrimPrefix(cookie.Host, ".")
	scheme := "http"
	if cookie.Secure {
		scheme = "https"
	}
	path := "/"
	if strings.HasPrefix(cookie.Path, "/") {
		path = cookie.Path
	}
	details := CookieDetails{
		URL:      scheme + "://" + host + path,
		Name:     cookie.Name,
		Value:    cookie.Value,
		Path:     cookie.Path,
		Secure:   cookie.Secure,
		HTTPOnly: cookie.HTTPOnly,
		SameSite: cookie.SameSite,
	}
	// A host without a leading dot is host-only, which leaving the domain out keeps.
	if strings.HasPrefix(cookie.Host, ".") {
		scope := cookie.Host
		details.Domain = &scope
	}
	if cookie.Expires != nil {
		expires := *cookie.Expires
		details.ExpirationDate = &expires
	}
	return details
}

// ReplaceCookies replaces sites' cookies in the panel's session with cookies, answering how many it set.
type ReplaceCookies func(ctx context.Context, cookies []CookieDetails, sites []string) (int, error)

// ImportBrowserSites reads those sites' cookies from the profile and hands them to replace.
func ImportBrowserSites(ctx context.Context, sourceID string, sites []string, replace ReplaceCookies) (*domain.BrowserImportResult, error) {
	cookies, skipped, err := readImportCookies(ctx, sourceID, sites)
	if err != nil {
		return nil, err
	}
	details := make([]CookieDetails, len(cookies))
	for index, cookie := range cookies {
		details[index] = CookieDetailsOf(cookie)
	}
	imported, err := replace(ctx, details, sites)
	if err != nil {
		return nil, err
	}
	return &domain.BrowserImportResult{Imported: float64(imported), Skipped: float64(skipped + len(cookies) - imported)}, nil
}
