#!/bin/sh
# Never inherit a live pane or a public URL/token into the sandbox.
set -eu
exec env -u TMUX -u TMUX_PANE -u SOPHONZ_URL -u SOPHONZ_TOKEN python3 "$(dirname "$0")/osc7501-sandbox.py" "$@"
