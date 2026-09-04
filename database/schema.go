package database

import _ "embed"

// Schema is the single canonical fresh-database definition shipped with the
// scheduler schema bootstrap command.
//
//go:embed schema.sql
var Schema string
