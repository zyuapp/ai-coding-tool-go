package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/mattn/go-sqlite3"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// ProfileDatabase copies a profile's SQLite files for an import. It is platform.ProfileDatabase.
type ProfileDatabase struct{}

var _ platform.ProfileDatabase = ProfileDatabase{}

func readOnlyURI(file string) string {
	return (&url.URL{Scheme: "file", Path: file, RawQuery: "mode=ro"}).String()
}

func rawConn(ctx context.Context, db *sql.DB, use func(*sqlite3.SQLiteConn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		sqliteConn, ok := driverConn.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("not a SQLite connection")
		}
		return use(sqliteConn)
	})
}

// Backup copies a live SQLite file with SQLite's online backup, opening the source read only.
func (ProfileDatabase) Backup(from, to string) error {
	registerDriver()
	source, err := sql.Open(driverName, readOnlyURI(from))
	if err != nil {
		return err
	}
	defer source.Close()
	_ = os.Remove(to)
	destination, err := sql.Open(driverName, to)
	if err != nil {
		return err
	}
	defer destination.Close()
	ctx := context.Background()
	return rawConn(ctx, destination, func(target *sqlite3.SQLiteConn) error {
		return rawConn(ctx, source, func(origin *sqlite3.SQLiteConn) error {
			backup, err := target.Backup("main", origin, "main")
			if err != nil {
				return err
			}
			for {
				done, err := backup.Step(-1)
				if err != nil {
					_ = backup.Close()
					return err
				}
				if done {
					break
				}
			}
			return backup.Close()
		})
	})
}

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
