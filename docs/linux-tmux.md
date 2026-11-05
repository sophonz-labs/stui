# Linux tmux runtime and terminal tests

Linux releases carry a pinned tmux 3.8, enabled only with `--embedded-tmux`.
Without that option, the existing system-tmux behavior remains the default. Draft 2026-11-05.

## Selecting the executable

```sh
stui                           # system tmux
stui --embedded-tmux           # bundled tmux (Linux release)
stui --embedded-tmux tmux -V    # selected client version
stui --embedded-tmux tmux display-message -p '#{version}' # existing server version
```

Put the option before the subcommand. Child processes inherit the selection,
including stui's background server. A running stui background server with a
different selection must first be stopped with `stui stop`; that command stops
stui's background service, not the tmux sessions. Start stui again with the
intended option. Nothing here automatically kills a tmux server.

**Selecting a newer executable does not upgrade a running tmux server.**
Socket selection remains unchanged: tmui honors `-L`, `-S` and `$TMUX`; stui
uses its normal default server (`TMUX_TMPDIR` can isolate it). Check the server
version separately. Retain or finish existing work before deliberately stopping
an old server. A new, separate test server avoids interrupting it.

For an isolated Linux test outside tmux, use a new temporary directory for
`TMUX_TMPDIR`; for stui also use a separate `STUI_HOME` so an already running
background service is not reused. These variables are for a test invocation,
not persistent changes to your login environment.

## Contents and portability

The Linux build uses Alpine's musl toolchain and statically links tmux's libraries.
The build rejects ELF interpreters and dynamic `NEEDED` libraries. This produces
one binary per CPU (`amd64`, `arm64`), rather than one per Linux distribution.
Supported kernels and PTY availability still matter; static linking is not a
promise that every Linux environment works.

The Go executable embeds a compressed archive with tmux, terminfo, license notices
and a dependency-package inventory. On selection, it is unpacked atomically under
`$XDG_CACHE_HOME/stui/tmux/<sha256>` (normally `~/.cache/stui/tmux/<sha256>`).
The directory needs to be writable and executable; set `XDG_CACHE_HOME` to an
appropriate location on a host with a `noexec` cache mount. There is no runtime
network download. The extraction does not prune files used by running servers.

The selected executable is put first on the child PATH. Popups receive its absolute
path because an outer tmux server may have an older PATH. Bundled terminfo is added
to `TERMINFO_DIRS`, preserving custom entries and the system search path.
macOS and ordinary source builds still use system tmux; requesting this option
without a payload fails explicitly.

## Build and test

```sh
sh scripts/bundle-linux.sh amd64  # or arm64; Docker/buildx with that CPU available
CGO_ENABLED=0 go build -tags bundled_tmux -o /tmp/stui .
/tmp/stui --embedded-tmux tmux -V
```

The Dockerfile pins the tmux 3.8 source URL and SHA-256 from its upstream release.
It builds dependencies from the Alpine 3.22 package repository and records their
resolved versions in `build-packages.txt`; those package versions are not a
bit-for-bit reproducibility guarantee. The archive is generated, not committed.

`linux-terminal` runs on GitHub-hosted Ubuntu 22.04 and 24.04 amd64, and Ubuntu
24.04 arm64. It builds both system and bundled variants, then uses real PTYs for:

- tmux alone, the UI outside tmux, and the UI inside tmux with popup attach;
- UI session and window creation, repeated attach/detach and terminal resizing;
- seeing typed characters **before Enter**, then verifying executed output;
- surviving multiple refreshes and checking the server stays alive;
- exact terminal-settings restoration after exiting the UI.

It covers `xterm-256color` and `screen-256color`. Missing tmux, a crash, lost input,
wrong echo state or a timeout fails the job. Additional network-disabled containers
check bundled executable startup on Alpine, Debian and Rocky Linux; these startup
checks do not replace the PTY tests or establish compatibility with every version.

To rerun the PTY test with a built binary (Python 3 and `pexpect` required):

```sh
python3 scripts/attach-smoke.py --product stui --binary /tmp/stui \
  --embedded --mode popup --term xterm-256color --output /tmp/stui-attach-evidence
```

Use `--mode direct` or `outside` for the other paths. Omit `--embedded` to compare
with system tmux. `--rounds` (default 3) and `--interval` (default 0.5 seconds per
input cycle) allow longer runs. Artifacts contain `terminal.log`, `events.log`,
tmux's `-vv` logs, exit/tty evidence and runtime metadata. The harness isolates
home, configuration, state and tmux sockets and never archives stui's token file.
Do not publish logs from real private sessions; the CI fixtures contain only
synthetic input.

Release packaging depends on this workflow and embeds the tested runtime archive.
This change is infrastructure for diagnosis; it does not establish that tmux 3.8
fixes any particular uncollected field crash.
