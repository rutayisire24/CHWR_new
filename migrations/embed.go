// Package migrations carries the schema as files embedded in the binary, so a
// deployed server can bring an empty database up to date without psql.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
