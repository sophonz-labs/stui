//go:build linux && bundled_tmux

package tmuxruntime

import _ "embed"

//go:embed tmux.tar.gz
var payload []byte
