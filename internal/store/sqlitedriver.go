// Package store provides a custom SQLite driver ("sqlite-fk") that wraps
// modernc.org/sqlite and automatically enables foreign keys + WAL journal
// mode on every new connection.  This is needed because PRAGMA foreign_keys
// is per-connection in SQLite, and the DSN-based _pragma parameter of
// modernc.org/sqlite is not reliably applied in all driver versions.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"

	sqlite "modernc.org/sqlite"
)

// driverNames we register — both "sqlite-fk" (our preferred name) and
// "sqlite3" (for compatibility with whatsmeow's sqlstore, which uses
// the dialect string to select SQL flavour AND as the driver name in
// sql.Open).  We skip a name if it's already registered.
func init() {
	for _, name := range []string{"sqlite-fk", "sqlite3"} {
		func(n string) {
			defer func() { recover() }() // sql.Register panics on duplicate
			sql.Register(n, &fkDriver{})
		}(name)
	}
}

// fkDriver wraps the modernc.org/sqlite driver to enable foreign keys
// and WAL journal mode on every new connection.
type fkDriver struct{}

func (d *fkDriver) Open(dsn string) (driver.Conn, error) {
	realDriver := &sqlite.Driver{}
	conn, err := realDriver.Open(dsn)
	if err != nil {
		return nil, err
	}

	// Enable foreign keys and WAL mode on this connection.
	if execer, ok := conn.(driver.ExecerContext); ok {
		_, _ = execer.ExecContext(context.Background(), "PRAGMA foreign_keys = ON", nil)
		_, _ = execer.ExecContext(context.Background(), "PRAGMA journal_mode = WAL", nil)
	}

	return conn, nil
}

// OpenDB is a convenience helper that opens a SQLite database with foreign
// keys and WAL already enabled via the "sqlite-fk" driver.
func OpenDB(path string) (*sql.DB, error) {
	return sql.Open("sqlite-fk", fmt.Sprintf("file:%s", path))
}
