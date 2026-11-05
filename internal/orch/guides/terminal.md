# stui terminal operations

Use this topic to inspect or operate the local terminal and UI. Draft 2026-11-06.

Open `stui` or `stui ui sessions`; `?` shows the keys for the current view.
Sessions lead to windows and panes. `a` attaches, and the displayed return hint
explains how to return. `stui ui agents` and `stui ui orch` show agents and runs.
A worker's `rename` changes its label, not its tmux session name.

For tmux commands, first identify the server and target. stui uses the default
local tmux server (under `TMUX_TMPDIR` when set); a shell's inherited `TMUX` can
name another server. Inspect `tmux list-sessions` and `tmux list-panes -a` on the
intended server, and use explicit targets. Do not kill a server to refresh a
list or assume a pane id from an earlier run still names the same agent.

`stui config show` and `stui config path` inspect the UI settings; `stui config
edit` changes them. `stui ext list` inspects UI extensions, while `stui ext
install` installs one. These extensions add views/actions and are different from
the coding-agent skills installed by `stui plugin install`.

`stui ci --help` describes the checkout's GitHub Actions commands. Only use CI
mutations, terminal input or process termination within the requested task.
For an attach/input problem, record the stui and tmux versions, TERM, whether
stui is inside tmux, and the exact error. A client returning to the UI is not
by itself proof that the tmux server crashed. `stui stop` stops stui's background
server; it does not upgrade or terminate an existing tmux server.
