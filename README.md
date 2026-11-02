# stui

```text
             /$$               /$$
            | $$              |__/
  /$$$$$$$ /$$$$$$   /$$   /$$ /$$
 /$$_____/|_  $$_/  | $$  | $$| $$
|  $$$$$$   | $$    | $$  | $$| $$
 \____  $$  | $$ /$$| $$  | $$| $$
 /$$$$$$$/  |  $$$$/|  $$$$$$/| $$
|_______/    \___/   \______/ |__/
```

A terminal UI for tmux and the coding agents running in it. It shows your sessions, windows and panes, which Claude Code, Codex, opencode or Pi agents are idle, working or asking for you, the GitHub Actions state of each pane's repository, and the orchestration runs where one agent hands work to others. It attaches to any of them and comes back when you detach.

Everything stays on this machine. stui runs a small background server on `127.0.0.1` that watches tmux and the agents' hooks; the UI and the CLI talk to it. It makes two kinds of network call: `gh` asks GitHub for CI runs, and the usage poller asks Anthropic and OpenAI for your plan limits (see [Usage limits](#usage-limits); `stui usage poll off` stops it).

```
OS:    darwin/arm64                     Agents: 1 working 1 asking 2 idle               <enter> output                          stui
Mux:   tmux 3.8                         Guard:  off                                     <a> attach to it                         dev
Panes: 7 in 3 sessions                                                                  <c> its CI
CI:    1 failed                                                                         <o> open CI run
                                                                                        <1> sessions
                                                                                        <2> agents
┌──────────────────────────────────────────────────── Agents(this machine)[4] ─────────────────────────────────────────────────────┐
│ PANE SESSION WINDOW  AGENT  STATE   TURN SUB CI       ORCH    TITLE                                                              │
│ %2   work    2 web   claude asking  18s      ✗ failed         trailing commas in the lexer                                       │
│ %1   work    1 api   claude working 16s      ✗ failed         fix the parser tests                                               │
│ %3   work    3 docs  claude done    17s                       write the changelog                                                │
│ %5   work    4 tests claude done    22s      ✗ failed tests ✓ make the parser tests pass                                         │
│                                                                                                                                  │
```

More screens are in [docs/captures](docs/captures).

## Without st / with st

stui needs no st, no account and no relay: install it, run `stui`, and everything above works on this machine alone. If you use st, use `st ui` instead; it is the same screen with your other machines in it. The two keep separate state (`~/.stui` here) and do not read each other's runs, so a run started with `st` shows in `st ui`, not in stui.

## Install

```sh
curl -fsSL https://dl.sophonz.io/stui/install.sh | sh
```

It puts `stui` in `~/.local/bin` after checking its SHA-256 (`STUI_INSTALL_DIR` puts it elsewhere, `STUI_VERSION=v0.1.0` pins a release). With Go:

```sh
go install github.com/sophonz-labs/stui@latest
```

Or download `stui-<os>-<arch>.tar.gz` (macOS and Linux, amd64 and arm64) and `checksums.txt` from the releases page.

Then let the agents report their turns to stui:

```sh
stui hooks install          # Claude Code, Codex, opencode, Pi and others it finds
stui hooks status
```

Without hooks stui still reads the agents' screens, but hooks are what tell working from asking reliably.

## Usage

```sh
stui                  # the UI; starts the background server if needed
stui ui agents        # open on a view: sessions, agents, orch, ci, usage
stui status           # the server and every agent's hooks
stui stop             # stop the server
stui --help
```

`enter` goes down a level (sessions, windows, panes, a pane's output), `esc` comes back. `?` lists every key.

## Keys

| Key | Where | Does |
|---|---|---|
| `1`-`5` | everywhere | sessions, agents, orch, ci, usage |
| `j` `k` `↑` `↓`, `g` `G`, `ctrl-b` `ctrl-f` | everywhere | move, top and bottom, a page |
| `enter` | lists | into the row; in the CI view, open the run |
| `esc`, `q` | everywhere | back (or clear the filter); `q` quits at the top |
| `/` | lists | filter the rows |
| `a` | sessions, windows, panes, agents, orch | attach (see below) |
| `c` | panes, agents | that pane's CI runs |
| `o` | panes, agents, CI | open the CI run in the browser |
| `n` | sessions, windows | new session; new window, optionally with an agent |
| `ctrl-d` | sessions, windows, orch | kill or stop (asks first) |
| `r` `d` | orch | read a worker's answer, its diff |
| `:` | everywhere | a view, or any stui command: `:worker list`, `:orch history` |
| `ctrl-r` | everywhere | refresh now (it refreshes every 2 seconds, CI every 10) |

States: `working` is teal, `asking` (the agent waits for a person) is orange, `idle` and `done` are plain.

## Attach and come back

Before attaching, stui says how to get back, with your real prefix key.

- Outside tmux, stui runs `tmux attach`. Detach (`prefix d`) and stui is back.
- Inside tmux 3.2 or later, it opens the session in a full-size `display-popup`. `prefix d` detaches the popup's client, the popup closes, and stui is where it was.
- Attaching to the session stui itself runs in switches to that window; `prefix l` returns.
- Inside tmux older than 3.2 it uses `switch-client`; `prefix L` or `prefix s` returns.

Attaching to a pane selects its window and the pane first.

## CI

For every pane, stui finds the git checkout it is in, the GitHub repository of its remote and the branch checked out, and asks `gh run list` for its runs (each repository at most every 5 seconds). The CI column and the CI view show each workflow's latest run on that branch: `● running`, `✓ passed`, `✗ failed`. `o` opens the run that matters most (a failed one, else a running one) with `open` on macOS or `xdg-open` on Linux.

stui never sees a GitHub token: `gh` holds it. Run `gh auth login` once. From the command line:

```sh
stui ci                   # this commit's runs
stui ci log --failed      # the failed job's log
```

## Orchestration

One agent (the coordinator) starts other agents as workers in their own tmux windows, hands them tasks, waits, and reads their reports. The commands are for the agents; `stui plugin install` teaches Claude Code and Codex to use them.

```sh
stui orch start --objective "make the parser tests pass"
stui worker start --agent claude --name tests --task task.md
stui worker wait --check-in 3m
stui worker read tests
stui worker send tests --file followup.md
stui worker list
```

A worker reports with `stui worker done --summary "<what it did, how it checked>"`. Workers can get their own git worktree (`--worktree`), owned paths (`--owns`), tasks with dependencies (`stui task`), and decisions that need a person (`stui gate`). See [docs/orchestration.md](docs/orchestration.md) and [docs/captures/orchestration.txt](docs/captures/orchestration.txt).

## Usage limits

The usage view and the header show how much of each plan limit Claude Code and Codex have used, and when it resets. stui gets the numbers two ways, and shows the newer:

- **It asks the provider.** Every five minutes the running stui reads the login the CLI already keeps on this machine (Claude Code: the macOS keychain item `Claude Code-credentials`, or `~/.claude/.credentials.json`; codex: `~/.codex/auth.json`) and makes the same request that CLI's own `/usage` makes: `GET https://api.anthropic.com/api/oauth/usage` and `GET https://chatgpt.com/backend-api/wham/usage`. The token is sent only to its own provider, never written anywhere, never logged and never refreshed; an expired one is skipped until the CLI signs in again. What comes back (percentages and reset times) is kept in `~/.stui/usage`.
- **The agent hands it over.** `stui hooks install --usage` makes `stui statusline` Claude Code's status line, which receives the limits on every turn; Codex writes them into its session files.

```sh
stui usage              # the limits, from the command line
stui usage poll off     # stop asking the providers; only what the agents hand over is shown
stui usage poll on
```

An API-key login has no plan limits, so there is nothing to show for it.

## Files

stui keeps its state in `~/.stui` (`STUI_HOME` moves it): the server's address and token, the runs, briefs and inboxes, and the server's log. `stui hooks install` edits the agents' own settings files and keeps a backup next to each.

## Requirements

- tmux 3.0 or later on `PATH`. Attaching in a popup needs 3.2.
- macOS or Linux.
- `gh` for CI (optional).
- `git` for worktrees (optional).

## License

Apache-2.0. See [LICENSE](LICENSE).
