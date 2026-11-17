// Package migrations embeds the SQL schema files into the API binary so a
// deploy carries its own schema and applies it at startup (see db.Migrate).
// Files are applied in filename order, so keep the numeric prefix.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
