package browser

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// SameSite is a cookie's same-site policy, named as Electron names it.
type SameSite string

const (
	SameSiteUnspecified   SameSite = "unspecified"
	SameSiteNoRestriction SameSite = "no_restriction"
	SameSiteLax           SameSite = "lax"
	SameSiteStrict        SameSite = "strict"
)

// StoredCookie is one cookie as another browser stored it, already decrypted. Expires is in Unix
// seconds, nil for a session cookie.
type StoredCookie struct {
	Host     string
	Name     string
	Value    string
	Path     string
	Secure   bool
	HTTPOnly bool
	Expires  *float64
	SameSite SameSite
}

type CookieFamily string

const (
	FamilyChromium CookieFamily = "chromium"
	FamilyFirefox  CookieFamily = "firefox"
)

// ChromiumKeys answers the keys a Chromium cookie of version "v10" or "v11" may be encrypted under,
// read only when one is needed.
type ChromiumKeys func(ctx context.Context, version string) ([][]byte, error)

// chromiumEpochOffset is the seconds between 1601, where Chromium counts from, and 1970.
const chromiumEpochOffset = 11_644_473_600

var sameSites = []SameSite{SameSiteNoRestriction, SameSiteLax, SameSiteStrict}

// hashedValuesVersion: Chromium prefixes a value with the SHA-256 of its host from this cookie
// database version on.
const hashedValuesVersion = 24

var cbcIV = bytes.Repeat([]byte{0x20}, 16)

// ChromiumKey is a Chromium key from the password its keyring holds.
func ChromiumKey(password string, iterations int) []byte {
	key, _ := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), iterations, 16)
	return key
}

func sameSiteOf(value int64) SameSite {
	if value >= 0 && value < int64(len(sameSites)) {
		return sameSites[value]
	}
	return SameSiteUnspecified
}

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	return target.Close()
}

// withCopy reads file from a private copy that is removed afterwards: the browser keeps its
// database open and may be writing to it, so the file and its write-ahead log are copied.
func withCopy[T any](ctx context.Context, file string, read func(*sql.Conn) (T, error)) (T, error) {
	var zero T
	folder, err := os.MkdirTemp("", "aic-cookies-")
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(folder)
	copied := filepath.Join(folder, "cookies.sqlite")
	if err := copyFile(file, copied); err != nil {
		return zero, err
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		_ = copyFile(file+suffix, copied+suffix)
	}
	database, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: copied}).EscapedPath())
	if err != nil {
		return zero, err
	}
	defer database.Close()
	conn, err := database.Conn(ctx)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	return read(conn)
}

func tableColumns(ctx context.Context, conn *sql.Conn, table string) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	nameAt := -1
	for index, name := range names {
		if name == "name" {
			nameAt = index
		}
	}
	found := map[string]bool{}
	values := make([]any, len(names))
	for rows.Next() {
		pointers := make([]any, len(names))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		if nameAt >= 0 {
			found[textOf(values[nameAt])] = true
		}
	}
	return found, rows.Err()
}

func textOf(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case []byte:
		return string(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return ""
	}
}

func numberOf(value any) float64 {
	switch value := value.(type) {
	case int64:
		return float64(value)
	case float64:
		return value
	case string:
		number, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return math.NaN()
		}
		return number
	case []byte:
		return numberOf(string(value))
	default:
		return 0
	}
}

// chromiumExpiry turns Chromium's microsecond times, past what a float64 holds exactly, into Unix
// seconds inside SQLite.
const chromiumExpiry = "CASE WHEN expires_utc = 0 THEN 0 ELSE expires_utc / 1000000 - 11644473600 END"

func chromiumFilter(ctx context.Context, conn *sql.Conn) (string, error) {
	columns, err := tableColumns(ctx, conn, "cookies")
	if err != nil {
		return "", err
	}
	// Cookies partitioned under another site's frame have nowhere to go in a session without partitions.
	partitioned := ""
	if columns["top_frame_site_key"] {
		partitioned = "AND top_frame_site_key = ''"
	}
	return "WHERE (expires_utc = 0 OR " + chromiumExpiry + " > ?) " + partitioned, nil
}

// firefoxExpiry: Firefox stored expiry in seconds and later moved to milliseconds.
func firefoxExpiry(expiry float64) float64 {
	if expiry > 100_000_000_000 {
		return expiry / 1000
	}
	return expiry
}

func nowSeconds() float64 { return float64(nowMs()) / 1000 }

// ReadCookieHosts counts the unexpired cookies each host holds, which is all listing sites needs:
// nothing is decrypted. now is in Unix seconds.
func ReadCookieHosts(ctx context.Context, family CookieFamily, file string, now float64) ([]domain.BrowserCookieHost, error) {
	return withCopy(ctx, file, func(conn *sql.Conn) ([]domain.BrowserCookieHost, error) {
		hosts := []domain.BrowserCookieHost{}
		if family == FamilyFirefox {
			rows, err := conn.QueryContext(ctx, "SELECT host, expiry FROM moz_cookies WHERE originAttributes = ''")
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			index := map[string]int{}
			for rows.Next() {
				var host, expiry any
				if err := rows.Scan(&host, &expiry); err != nil {
					return nil, err
				}
				if firefoxExpiry(numberOf(expiry)) <= now {
					continue
				}
				name := textOf(host)
				if at, ok := index[name]; ok {
					hosts[at].Cookies++
					continue
				}
				index[name] = len(hosts)
				hosts = append(hosts, domain.BrowserCookieHost{Host: name, Cookies: 1})
			}
			return hosts, rows.Err()
		}
		filter, err := chromiumFilter(ctx, conn)
		if err != nil {
			return nil, err
		}
		rows, err := conn.QueryContext(ctx, "SELECT host_key AS host, COUNT(*) AS cookies FROM cookies "+filter+" GROUP BY host_key", now)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var host, count any
			if err := rows.Scan(&host, &count); err != nil {
				return nil, err
			}
			hosts = append(hosts, domain.BrowserCookieHost{Host: textOf(host), Cookies: numberOf(count)})
		}
		return hosts, rows.Err()
	})
}

// DecryptChromiumValue decrypts one Chromium value, or reports false when none of the keys open it.
func DecryptChromiumValue(encrypted []byte, host string, keys [][]byte, hashed bool) (string, bool) {
	if len(encrypted) < 3 {
		return "", false
	}
	body := encrypted[3:]
	hostHash := sha256.Sum256([]byte(host))
	for _, key := range keys {
		plain, ok := decryptCBC(key, body)
		if !ok {
			continue
		}
		if hashed {
			if len(plain) < 32 || !bytes.Equal(plain[:32], hostHash[:]) {
				continue
			}
			plain = plain[32:]
		}
		// A wrong key can still unpad cleanly; what it yields is not text a cookie could hold.
		if !cookieText(plain) {
			continue
		}
		return string(plain), true
	}
	return "", false
}

func decryptCBC(key, body []byte) ([]byte, bool) {
	block, err := aes.NewCipher(key)
	if err != nil || len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return nil, false
	}
	plain := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, cbcIV).CryptBlocks(plain, body)
	padding := int(plain[len(plain)-1])
	if padding == 0 || padding > aes.BlockSize {
		return nil, false
	}
	for _, value := range plain[len(plain)-padding:] {
		if int(value) != padding {
			return nil, false
		}
	}
	return plain[:len(plain)-padding], true
}

func cookieText(value []byte) bool {
	for len(value) > 0 {
		r, size := utf8.DecodeRune(value)
		if r == utf8.RuneError || r < 0x20 || r == 0x7f {
			return false
		}
		value = value[size:]
	}
	return true
}

func onAnySite(host string, sites []string) bool {
	for _, site := range sites {
		if domain.CookieOnSite(host, site) {
			return true
		}
	}
	return false
}

type chromiumRow struct {
	host, name, value string
	encrypted         []byte
	path              string
	expires           float64
	secure, httpOnly  bool
	sameSite          int64
}

// ReadCookies reads every unexpired cookie for sites. Ones no key opens are counted rather than
// returned. now is in Unix seconds.
func ReadCookies(ctx context.Context, family CookieFamily, file string, sites []string, keys ChromiumKeys, now float64) ([]StoredCookie, int, error) {
	if family == FamilyFirefox {
		cookies, err := withCopy(ctx, file, func(conn *sql.Conn) ([]StoredCookie, error) {
			rows, err := conn.QueryContext(ctx, "SELECT host, name, value, path, expiry, isSecure, isHttpOnly, sameSite FROM moz_cookies WHERE originAttributes = ''")
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			cookies := []StoredCookie{}
			for rows.Next() {
				var host, name, value, path, expiry, secure, httpOnly, sameSite any
				if err := rows.Scan(&host, &name, &value, &path, &expiry, &secure, &httpOnly, &sameSite); err != nil {
					return nil, err
				}
				expires := firefoxExpiry(numberOf(expiry))
				if !onAnySite(textOf(host), sites) || !(expires > now) {
					continue
				}
				cookiePath := textOf(path)
				if cookiePath == "" {
					cookiePath = "/"
				}
				floored := math.Floor(expires)
				cookies = append(cookies, StoredCookie{
					Host:     textOf(host),
					Name:     textOf(name),
					Value:    textOf(value),
					Path:     cookiePath,
					Secure:   numberOf(secure) != 0,
					HTTPOnly: numberOf(httpOnly) != 0,
					Expires:  &floored,
					SameSite: sameSiteOf(int64(numberOf(sameSite))),
				})
			}
			return cookies, rows.Err()
		})
		return cookies, 0, err
	}
	type read struct {
		rows    []chromiumRow
		version float64
	}
	found, err := withCopy(ctx, file, func(conn *sql.Conn) (read, error) {
		var version any
		if err := conn.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'version'").Scan(&version); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return read{}, err
		}
		filter, err := chromiumFilter(ctx, conn)
		if err != nil {
			return read{}, err
		}
		rows, err := conn.QueryContext(ctx, "SELECT host_key AS host, name, value, encrypted_value AS encrypted, path, "+chromiumExpiry+" AS expires, is_secure AS secure, is_httponly AS httpOnly, samesite AS sameSite FROM cookies "+filter, now)
		if err != nil {
			return read{}, err
		}
		defer rows.Close()
		result := read{rows: []chromiumRow{}, version: numberOf(version)}
		for rows.Next() {
			var host, name, value, encrypted, path, expires, secure, httpOnly, sameSite any
			if err := rows.Scan(&host, &name, &value, &encrypted, &path, &expires, &secure, &httpOnly, &sameSite); err != nil {
				return read{}, err
			}
			row := chromiumRow{
				host: textOf(host), name: textOf(name), value: textOf(value), path: textOf(path),
				expires: numberOf(expires), secure: numberOf(secure) != 0, httpOnly: numberOf(httpOnly) != 0, sameSite: int64(numberOf(sameSite)),
			}
			switch blob := encrypted.(type) {
			case []byte:
				row.encrypted = blob
			case string:
				row.encrypted = []byte(blob)
			}
			if onAnySite(row.host, sites) {
				result.rows = append(result.rows, row)
			}
		}
		return result, rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	keyring := map[string][][]byte{}
	cookies := []StoredCookie{}
	skipped := 0
	for _, row := range found.rows {
		value := row.value
		if len(row.encrypted) > 0 {
			prefix := ""
			if len(row.encrypted) >= 3 {
				prefix = string(row.encrypted[:3])
			}
			if prefix != "v10" && prefix != "v11" {
				skipped++
				continue
			}
			held, ok := keyring[prefix]
			if !ok {
				held, err = keys(ctx, prefix)
				if err != nil {
					return nil, 0, err
				}
				keyring[prefix] = held
			}
			decrypted, ok := DecryptChromiumValue(row.encrypted, row.host, held, found.version >= hashedValuesVersion)
			if !ok {
				skipped++
				continue
			}
			value = decrypted
		}
		cookiePath := row.path
		if cookiePath == "" {
			cookiePath = "/"
		}
		var expires *float64
		if row.expires != 0 && !math.IsNaN(row.expires) {
			expires = &row.expires
		}
		cookies = append(cookies, StoredCookie{
			Host: row.host, Name: row.name, Value: value, Path: cookiePath,
			Secure: row.secure, HTTPOnly: row.httpOnly, Expires: expires, SameSite: sameSiteOf(row.sameSite),
		})
	}
	return cookies, skipped, nil
}
