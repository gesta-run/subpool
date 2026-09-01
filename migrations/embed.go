package migrations

import "embed"

// Files contains the ordered database migrations shipped with Subpool.
//
//go:embed *.sql
var Files embed.FS
