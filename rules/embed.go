// Package rules contains configuration data, without Go gameplay policies.
package rules

import "embed"

// Files supplies packaged defaults.
// Production may read another directory with UNO_RULES_DIR.
//
//go:embed *.json
var Files embed.FS
