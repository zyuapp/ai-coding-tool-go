package domain

import (
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// BrowserCookieHost is one cookie host a profile holds, and how many cookies it holds for it.
type BrowserCookieHost struct {
	Host    string
	Cookies float64
}

const MaxImportSites = 500

func hostName(host string) string {
	return strings.ToLower(strings.TrimPrefix(host, "."))
}

func ImportSites(hosts []BrowserCookieHost) []*BrowserImportSite {
	type named struct {
		name    string
		cookies float64
	}
	names := []named{}
	for _, entry := range hosts {
		if name := hostName(entry.Host); name != "" {
			names = append(names, named{name, entry.Cookies})
		}
	}
	slices.SortStableFunc(names, func(left, right named) int {
		return strings.Count(left.name, ".") - strings.Count(right.name, ".")
	})
	order := []string{}
	sites := map[string]float64{}
	for _, entry := range names {
		labels := strings.Split(entry.name, ".")
		parent := entry.name
		for index := 1; index < len(labels); index++ {
			candidate := strings.Join(labels[index:], ".")
			if _, ok := sites[candidate]; ok {
				parent = candidate
				break
			}
		}
		if _, ok := sites[parent]; !ok {
			order = append(order, parent)
		}
		sites[parent] += entry.cookies
	}
	collator := collate.New(language.Und)
	slices.SortStableFunc(order, collator.CompareString)
	result := make([]*BrowserImportSite, len(order))
	for i, domain := range order {
		result[i] = &BrowserImportSite{Domain: domain, Cookies: sites[domain]}
	}
	return result
}

// CookieOnSite reports whether a cookie set for host belongs to site.
func CookieOnSite(host, site string) bool {
	name := hostName(host)
	return name == site || strings.HasSuffix(name, "."+site)
}

var importSitePattern = regexp.MustCompile(`^[a-z0-9.-]+$`)

func IsImportSite(value any) bool {
	site, ok := value.(string)
	return ok && site != "" && len(site) <= 253 && importSitePattern.MatchString(site) && !strings.HasPrefix(site, ".")
}

func IsImportSourceID(value any) bool {
	id, ok := value.(string)
	return ok && id != "" && utf16Length(id) <= 1024
}

func IsBrowserImportMemory(value any) bool {
	memory, ok := record(value)
	if !ok || !IsImportSourceID(memory["sourceId"]) {
		return false
	}
	sites, ok := memory["sites"].([]any)
	return ok && len(sites) > 0 && len(sites) <= MaxImportSites && !slices.ContainsFunc(sites, func(site any) bool { return !IsImportSite(site) })
}
