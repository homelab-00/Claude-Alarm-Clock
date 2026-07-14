// Package fonts embeds the vendored fonts shipped with the app.
//
// Go's //go:embed directive may not use ".." to reach outside the directory
// of the source file that declares it (nor may it follow a symlink), so the
// embed must live next to the font it embeds rather than in the internal/ui
// package that consumes it. internal/ui imports MonoBoldTTF instead.
package fonts

import _ "embed"

// MonoBoldTTF is JetBrains Mono Bold, SIL OFL 1.1. See OFL.txt in this
// directory -- the licence ships with the font.
//
//go:embed JetBrainsMono-Bold.ttf
var MonoBoldTTF []byte
