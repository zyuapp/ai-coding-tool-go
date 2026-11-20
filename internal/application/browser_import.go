package application

import (
	"fmt"
	"strconv"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var NoBrowserImport = BrowserImportState{Sources: nil, Picker: nil, Importing: false, Notice: nil, Error: nil, Last: nil}

// SourceName is what a notice calls a profile; nil source is "that browser".
func SourceName(source *domain.BrowserImportSource, sources []*domain.BrowserImportSource) string {
	if source == nil {
		return "that browser"
	}
	same := 0
	for _, item := range sources {
		if item.Browser == source.Browser {
			same++
		}
	}
	if same > 1 {
		return source.Browser + " (" + source.Profile + ")"
	}
	return source.Browser
}

func ImportNotice(result domain.BrowserImportResult, sites []string, from string) string {
	where := fmt.Sprintf("%d sites", len(sites))
	if len(sites) == 1 {
		where = sites[0]
	}
	noun := "cookies"
	if result.Imported == 1 {
		noun = "cookie"
	}
	copied := fmt.Sprintf("Copied %s %s for %s from %s.", strconv.FormatFloat(result.Imported, 'f', -1, 64), noun, where, from)
	if result.Skipped != 0 {
		return fmt.Sprintf("%s %s could not be read.", copied, strconv.FormatFloat(result.Skipped, 'f', -1, 64))
	}
	return copied
}
