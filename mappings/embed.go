// Package mappings embeds the resource-type-to-IAM-action database so the CLI
// ships as a single binary with no runtime file dependency.
//
// The YAML files beside this one are the database. They are intentionally at the
// repository root rather than under internal/: the mapping content is an open,
// community-maintained asset, and contributing a fix should not require reading
// any Go. See README.md and SCHEMA.md in this directory.
package mappings

import "embed"

//go:embed *.yaml
var FS embed.FS
