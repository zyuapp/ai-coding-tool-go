package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// ProfileDatabase copies a profile's SQLite files for an import. It is platform.ProfileDatabase.
type ProfileDatabase struct{}

var _ platform.ProfileDatabase = ProfileDatabase{}

// RepointAttachments rewrites the attachment paths the copied store's messages hold from one profile's
// attachments folder to another's, and counts its threads and projects.
func (ProfileDatabase) RepointAttachments(database, fromAttachments, toAttachments string) (platform.ImportReport, error) {
	registerDriver()
	db, err := sql.Open(driverName, database)
	if err != nil {
		return platform.ImportReport{}, err
	}
	defer db.Close()
	from, err := jsonx.Marshal(fromAttachments)
	if err != nil {
		return platform.ImportReport{}, err
	}
	to, err := jsonx.Marshal(toAttachments)
	if err != nil {
		return platform.ImportReport{}, err
	}
	fromText, toText := strings.Trim(string(from), `"`), strings.Trim(string(to), `"`)
	if fromText != toText {
		if _, err := db.Exec("UPDATE messages SET data = replace(data, ?, ?) WHERE instr(data, ?) > 0", fromText, toText, fromText); err != nil {
			return platform.ImportReport{}, fmt.Errorf("could not move attachment paths: %w", err)
		}
	}
	var report platform.ImportReport
	if err := db.QueryRow("SELECT count(*) FROM tasks").Scan(&report.Threads); err != nil {
		return report, err
	}
	if err := db.QueryRow("SELECT count(*) FROM projects").Scan(&report.Projects); err != nil {
		return report, err
	}
	return report, nil
}
