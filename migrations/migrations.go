// Package migrations embeds the goose SQL files so tests and binaries apply
// exactly the files that `make migrate` uses.
package migrations

import "embed"

// FS holds the migration files, compiled into the binary by go:embed
// (Go's counterpart to reading resources from the classpath).
//
//go:embed *.sql
var FS embed.FS
