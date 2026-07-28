package store

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func Migrations() (fs.FS, error) {
	return fs.Sub(migrationFiles, "migrations")
}
