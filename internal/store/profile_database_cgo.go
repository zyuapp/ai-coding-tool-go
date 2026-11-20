//go:build cgo

package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"

	"github.com/mattn/go-sqlite3"
)

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
