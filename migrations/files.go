// Package migrations contains the versioned PostgreSQL schema.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
