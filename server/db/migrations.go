// Package db embeds the goose migrations so the server binary can apply them without the source tree.
package db

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS
