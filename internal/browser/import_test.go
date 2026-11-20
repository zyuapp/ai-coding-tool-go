package browser

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestTheUserAgentNamesTheChromeThisBuildIs(t *testing.T) {
	identity := ChromeIdentityFor("150.0.7871.224")
	if identity.UserAgent != "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36" {
		t.Fatal(identity.UserAgent)
	}
	headers := ChromeHeaders("https://example.com/", map[string]string{}, identity)
	want := map[string]string{
		"Sec-CH-UA":          `"Chromium";v="150", "Google Chrome";v="150", "Not?A_Brand";v="24"`,
		"Sec-CH-UA-Mobile":   "?0",
		"Sec-CH-UA-Platform": `"macOS"`,
		"Accept-Language":    "en-US,en;q=0.9",
	}
	if !reflect.DeepEqual(headers, want) {
		t.Fatalf("got %v", headers)
	}
}

func TestChromiumsOwnHintsAreReplacedRatherThanSentAlongside(t *testing.T) {
	identity := ChromeIdentityFor("150.0.7871.224")
	headers := ChromeHeaders("https://example.com/", map[string]string{"sec-ch-ua": `"Chromium";v="150"`, "accept-language": "en-US"}, identity)
	count := func(name string) int {
		found := 0
		for key := range headers {
			if strings.EqualFold(key, name) {
				found++
			}
		}
		return found
	}
	if count("sec-ch-ua") != 1 || count("accept-language") != 1 || headers["Sec-CH-UA"] != `"Chromium";v="150", "Google Chrome";v="150", "Not?A_Brand";v="24"` {
		t.Fatalf("got %v", headers)
	}
}

func TestAHighEntropyHintGoesOutOnlyWhereTheSiteAskedForIt(t *testing.T) {
	identity := ChromeIdentityFor("150.0.7871.224")
	if _, ok := ChromeHeaders("https://example.com/", map[string]string{}, identity)["Sec-CH-UA-Full-Version-List"]; ok {
		t.Fatal("an unasked hint went out")
	}
	asked := ChromeHeaders("https://example.com/", map[string]string{"Sec-CH-UA-Full-Version-List": `"Chromium";v="150.0.7871.224"`}, identity)
	if asked["Sec-CH-UA-Full-Version-List"] != `"Chromium";v="150.0.7871.224", "Google Chrome";v="150.0.7871.224", "Not?A_Brand";v="24.0.0.0"` {
		t.Fatal(asked["Sec-CH-UA-Full-Version-List"])
	}
}

func TestHTTPCarriesNoClientHints(t *testing.T) {
	headers := ChromeHeaders("http://127.0.0.1:8080/", map[string]string{}, ChromeIdentityFor("150.0.7871.224"))
	if _, ok := headers["Sec-CH-UA"]; ok || headers["Accept-Language"] != "en-US,en;q=0.9" {
		t.Fatalf("got %v", headers)
	}
}

func TestSafariVersionReadsTheMajorAndMinor(t *testing.T) {
	if version, ok := SafariVersion("<dict><key>CFBundleShortVersionString</key>\n\t<string>26.1.2</string></dict>"); !ok || version != "26.1" {
		t.Fatal(version)
	}
	if _, ok := SafariVersion("<dict></dict>"); ok {
		t.Fatal("read a version from nothing")
	}
}

const testNow = 1_800_000_000
const chromiumEpoch = 11_644_473_600

func folder(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func encryptCookie(t *testing.T, value, host string, key []byte) []byte {
	t.Helper()
	hash := sha256.Sum256([]byte(host))
	plain := append(hash[:], value...)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, cbcIV).CryptBlocks(out, plain)
	return append([]byte("v10"), out...)
}

func chromiumTime(seconds int64) int64 { return (seconds + chromiumEpoch) * 1_000_000 }

func openDatabase(t *testing.T, file string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func exec(t *testing.T, database *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := database.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func chromiumStore(t *testing.T, key []byte) string {
	file := filepath.Join(folder(t), "Cookies")
	database := openDatabase(t, file)
	exec(t, database, `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
    INSERT INTO meta VALUES ('version', '24');
    CREATE TABLE cookies (host_key TEXT, top_frame_site_key TEXT, name TEXT, value TEXT, encrypted_value BLOB, path TEXT,
      expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, samesite INTEGER);`)
	insert := "INSERT INTO cookies VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?)"
	exec(t, database, insert, ".github.com", "", "logged_in", encryptCookie(t, "yes", ".github.com", key), "/", chromiumTime(testNow+3600), 1, 1, 1)
	exec(t, database, insert, "github.com", "", "user_session", encryptCookie(t, "abc123", "github.com", key), "/", 0, 1, 1, 2)
	exec(t, database, insert, "github.com", "", "stale", encryptCookie(t, "old", "github.com", key), "/", chromiumTime(testNow-10), 1, 0, 0)
	exec(t, database, insert, "github.com", "https://other.example", "partitioned", encryptCookie(t, "p", "github.com", key), "/", 0, 1, 0, 0)
	exec(t, database, insert, "example.com", "", "theme", encryptCookie(t, "dark", "example.com", key), "/", 0, 0, 0, -1)
	database.Close()
	return file
}

func TestListingAChromiumProfileCountsLiveCookiesPerHost(t *testing.T) {
	file := chromiumStore(t, ChromiumKey("secret", 1003))
	hosts, err := ReadCookieHosts(context.Background(), FamilyChromium, file, testNow)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(hosts, func(left, right domain.BrowserCookieHost) int { return strings.Compare(left.Host, right.Host) })
	want := []domain.BrowserCookieHost{{Host: ".github.com", Cookies: 1}, {Host: "example.com", Cookies: 1}, {Host: "github.com", Cookies: 1}}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("got %v", hosts)
	}
}

func TestChromiumCookiesForTheChosenSitesComeBackDecrypted(t *testing.T) {
	key := ChromiumKey("secret", 1003)
	file := chromiumStore(t, key)
	asked := []string{}
	cookies, skipped, err := ReadCookies(context.Background(), FamilyChromium, file, []string{"github.com"}, func(_ context.Context, version string) ([][]byte, error) {
		asked = append(asked, version)
		return [][]byte{ChromiumKey("wrong", 1003), key}, nil
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 || !slices.Equal(asked, []string{"v10"}) {
		t.Fatalf("skipped %d, asked %v: the key is fetched once, however many cookies need it", skipped, asked)
	}
	slices.SortFunc(cookies, func(left, right StoredCookie) int { return strings.Compare(left.Name, right.Name) })
	expires := float64(testNow + 3600)
	want := []StoredCookie{
		{Host: ".github.com", Name: "logged_in", Value: "yes", Path: "/", Secure: true, HTTPOnly: true, Expires: &expires, SameSite: SameSiteLax},
		{Host: "github.com", Name: "user_session", Value: "abc123", Path: "/", Secure: true, HTTPOnly: true, SameSite: SameSiteStrict},
	}
	if !reflect.DeepEqual(cookies, want) {
		t.Fatalf("got %+v", cookies)
	}
}

func TestACookieNoKeyOpensIsCountedAsSkipped(t *testing.T) {
	file := chromiumStore(t, ChromiumKey("secret", 1003))
	cookies, skipped, err := ReadCookies(context.Background(), FamilyChromium, file, []string{"github.com"}, func(context.Context, string) ([][]byte, error) {
		return [][]byte{ChromiumKey("wrong", 1003)}, nil
	}, testNow)
	if err != nil || len(cookies) != 0 || skipped != 2 {
		t.Fatalf("got %v, %d, %v", cookies, skipped, err)
	}
}

func TestFirefoxCookiesAreReadAsStoredLeavingContainerCookiesBehind(t *testing.T) {
	file := filepath.Join(folder(t), "cookies.sqlite")
	database := openDatabase(t, file)
	exec(t, database, `CREATE TABLE moz_cookies (originAttributes TEXT, name TEXT, value TEXT, host TEXT, path TEXT, expiry INTEGER,
    isSecure INTEGER, isHttpOnly INTEGER, sameSite INTEGER)`)
	insert := "INSERT INTO moz_cookies VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"
	exec(t, database, insert, "", "sid", "firefox-value", ".wikipedia.org", "/", testNow+60, 1, 1, 0)
	exec(t, database, insert, "", "ms", "millis", "en.wikipedia.org", "/wiki", (testNow+60)*1000, 0, 0, 1)
	exec(t, database, insert, "^userContextId=2", "work", "container", ".wikipedia.org", "/", testNow+60, 1, 1, 0)
	exec(t, database, insert, "", "gone", "expired", ".wikipedia.org", "/", testNow-60, 1, 1, 0)
	database.Close()

	hosts, err := ReadCookieHosts(context.Background(), FamilyFirefox, file, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hosts, []domain.BrowserCookieHost{{Host: ".wikipedia.org", Cookies: 1}, {Host: "en.wikipedia.org", Cookies: 1}}) {
		t.Fatalf("got %v", hosts)
	}
	cookies, _, err := ReadCookies(context.Background(), FamilyFirefox, file, []string{"wikipedia.org"}, nil, testNow)
	if err != nil {
		t.Fatal(err)
	}
	expires := float64(testNow + 60)
	want := []StoredCookie{
		{Host: ".wikipedia.org", Name: "sid", Value: "firefox-value", Path: "/", Secure: true, HTTPOnly: true, Expires: &expires, SameSite: SameSiteNoRestriction},
		{Host: "en.wikipedia.org", Name: "ms", Value: "millis", Path: "/wiki", Expires: &expires, SameSite: SameSiteLax},
	}
	if !reflect.DeepEqual(cookies, want) {
		t.Fatalf("got %+v", cookies)
	}
}

func writeFile(t *testing.T, at, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMacOSProfilesAreFoundUnderEachBrowsersOwnFolder(t *testing.T) {
	root := folder(t)
	support := filepath.Join(root, "Library", "Application Support")
	brave := filepath.Join(support, "BraveSoftware", "Brave-Browser")
	writeFile(t, filepath.Join(brave, "Local State"), `{"profile":{"info_cache":{"Default":{"name":"Personal"},"Profile 1":{"name":"Work"},"Profile 2":{"name":"Empty"}}}}`)
	writeFile(t, filepath.Join(brave, "Default", "Network", "Cookies"), "")
	writeFile(t, filepath.Join(brave, "Profile 1", "Cookies"), "")
	writeFile(t, filepath.Join(support, "com.operasoftware.Opera", "Network", "Cookies"), "")
	writeFile(t, filepath.Join(support, "Firefox", "profiles.ini"), "[Profile0]\nName=default-release\nIsRelative=1\nPath=Profiles/abc.default-release\n")
	writeFile(t, filepath.Join(support, "Firefox", "Profiles", "abc.default-release", "cookies.sqlite"), "")

	type found struct{ id, browser, profile, file string }
	got := []found{}
	for _, source := range Discover("darwin", root, "") {
		relative, _ := filepath.Rel(support, source.File)
		got = append(got, found{source.ID, source.Browser, source.Profile, relative})
	}
	want := []found{
		{"brave:0:Default", "Brave", "Personal", "BraveSoftware/Brave-Browser/Default/Network/Cookies"},
		{"brave:0:Profile 1", "Brave", "Work", "BraveSoftware/Brave-Browser/Profile 1/Cookies"},
		{"opera:0:.", "Opera", "Default", "com.operasoftware.Opera/Network/Cookies"},
		{"firefox:0:Profiles/abc.default-release", "Firefox", "default-release", "Firefox/Profiles/abc.default-release/cookies.sqlite"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestLinuxProfilesAreFoundInTheConfigFolderAndInSnapAndFlatpakInstalls(t *testing.T) {
	root := folder(t)
	writeFile(t, filepath.Join(root, ".config", "google-chrome", "Default", "Cookies"), "")
	writeFile(t, filepath.Join(root, ".var", "app", "com.brave.Browser", "config", "BraveSoftware", "Brave-Browser", "Default", "Cookies"), "")
	snap := filepath.Join(root, "snap", "firefox", "common", ".mozilla", "firefox")
	writeFile(t, filepath.Join(snap, "profiles.ini"), "[Profile0]\nName=default\nIsRelative=1\nPath=xyz.default\n")
	writeFile(t, filepath.Join(snap, "xyz.default", "cookies.sqlite"), "")

	got := [][2]string{}
	for _, source := range Discover("linux", root, "") {
		got = append(got, [2]string{source.ID, source.Browser})
	}
	want := [][2]string{{"chrome:0:Default", "Chrome"}, {"brave:1:Default", "Brave"}, {"firefox:2:xyz.default", "Firefox"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestLinuxAsksEachKeyringUnderTheNameTheBrowserStoresItsKeyBy(t *testing.T) {
	root := folder(t)
	writeFile(t, filepath.Join(root, ".var", "app", "com.vivaldi.Vivaldi", "config", "vivaldi", "Default", "Cookies"), "")
	writeFile(t, filepath.Join(root, ".config", "microsoft-edge", "Default", "Cookies"), "")
	writeFile(t, filepath.Join(root, ".config", "opera", "Default", "Cookies"), "")
	xdg := filepath.Join(root, ".var", "app", "org.mozilla.firefox", "config", "mozilla", "firefox")
	writeFile(t, filepath.Join(xdg, "profiles.ini"), "[Profile0]\nName=default\nIsRelative=1\nPath=abc.default\n")
	writeFile(t, filepath.Join(xdg, "abc.default", "cookies.sqlite"), "")

	got := [][3]string{}
	for _, source := range Discover("linux", root, "") {
		got = append(got, [3]string{source.Browser, source.Kind.Libsecret, source.Kind.Kwallet})
	}
	want := [][3]string{{"Microsoft Edge", "chromium", "Chromium"}, {"Vivaldi", "chrome", "Chrome"}, {"Opera", "chromium", "Chromium"}, {"Firefox", "", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestFirefoxsOwnDefaultProfileIsOfferedFirst(t *testing.T) {
	entries := FirefoxProfileEntries(strings.Join([]string{
		"[Profile1]", "Name=old", "IsRelative=1", "Path=Profiles/old",
		"[Profile0]", "Name=current", "IsRelative=1", "Path=Profiles/current",
		"[Install4F96D1932A9F858E]", "Default=Profiles/current",
	}, "\n"))
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry["Name"])
	}
	if !slices.Equal(names, []string{"current", "old"}) {
		t.Fatal(names)
	}
}

func TestACookieKeepsItsScope(t *testing.T) {
	expires := 1_900_000_000.0
	domainCookie := CookieDetailsOf(StoredCookie{Host: ".github.com", Name: "a", Value: "1", Path: "/", Secure: true, HTTPOnly: true, Expires: &expires, SameSite: SameSiteLax})
	if !reflect.DeepEqual(domainCookie, CookieDetails{URL: "https://github.com/", Name: "a", Value: "1", Domain: ptr(".github.com"), Path: "/", Secure: true, HTTPOnly: true, SameSite: SameSiteLax, ExpirationDate: &expires}) {
		t.Fatalf("got %+v", domainCookie)
	}
	hostCookie := CookieDetailsOf(StoredCookie{Host: "example.com", Name: "b", Value: "2", Path: "/app", SameSite: SameSiteUnspecified})
	if !reflect.DeepEqual(hostCookie, CookieDetails{URL: "http://example.com/app", Name: "b", Value: "2", Path: "/app", SameSite: SameSiteUnspecified}) {
		t.Fatalf("got %+v", hostCookie)
	}
}

func TestLinuxAsksTheNetworkWalletKWalletNames(t *testing.T) {
	calls := [][]string{}
	helpers := &KeyHelpers{}
	keys := ChromiumKeysFor(BrowserKind{Key: "chrome", Name: "Chrome", Family: FamilyChromium, Libsecret: "chrome", Kwallet: "Chrome"}, helpers, "linux",
		func(_ context.Context, command string, args []string, _ time.Duration) (string, ExecStatus) {
			calls = append(calls, append([]string{command}, args...))
			switch command {
			case "dbus-send":
				if slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, "kwalletd6") }) {
					return "", ExecFailed
				}
				return "  work-wallet\n", ExecOK
			case "secret-tool":
				return "", ExecMissing
			}
			return "kwallet-secret", ExecOK
		})
	ctx := context.Background()
	if got, _ := keys(ctx, "v10"); !reflect.DeepEqual(got, [][]byte{ChromiumKey("peanuts", 1)}) {
		t.Fatal("v10 is not Chromium's fixed password")
	}
	if got, _ := keys(ctx, "v11"); !reflect.DeepEqual(got, [][]byte{ChromiumKey("kwallet-secret", 1), ChromiumKey("", 1)}) {
		t.Fatal("v11 did not try the wallet and the empty password")
	}
	if !slices.Equal(helpers.Missing, []string{"secret-tool"}) {
		t.Fatal(helpers.Missing)
	}
	for _, call := range calls {
		if call[0] == "kwallet-query" && call[len(call)-1] != "work-wallet" {
			t.Fatal(call)
		}
	}
}

func TestAReadMacOSRefusesAsksForFullDiskAccess(t *testing.T) {
	refused := func() (int, error) { return 0, &os.PathError{Op: "open", Path: "Cookies", Err: syscall.EPERM} }
	_, err := ReadProfile("Chrome", refused, "darwin")
	var blocked *BrowserDataBlocked
	if !errors.As(err, &blocked) || !strings.Contains(blocked.Message, "Chrome") || !strings.Contains(blocked.Message, "Full Disk Access") {
		t.Fatal(err)
	}
	if _, err := ReadProfile("Chrome", refused, "linux"); !errors.Is(err, syscall.EPERM) {
		t.Fatal(err)
	}
	if _, err := ReadProfile("Chrome", func() (int, error) { return 0, errors.New("file is not a database") }, "darwin"); err == nil || !strings.Contains(err.Error(), "not a database") {
		t.Fatal(err)
	}
}

func TestDecryptingRejectsAWrongKeyThatStillUnpads(t *testing.T) {
	key := ChromiumKey("secret", 1003)
	encrypted := encryptCookie(t, "value", "host", key)
	if value, ok := DecryptChromiumValue(encrypted, "host", [][]byte{key}, true); !ok || value != "value" {
		t.Fatal(value)
	}
	if _, ok := DecryptChromiumValue(encrypted, "other", [][]byte{key}, true); ok {
		t.Fatal("a value for another host was accepted")
	}
}
