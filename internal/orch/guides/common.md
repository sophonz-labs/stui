# Using stui from a coding agent

Use stui for the user's requested work on this machine: inspecting the terminal,
checking agents, exchanging briefs, or coordinating workers. Start here and read
only the topic you need. Draft 2026-11-06.

## Environment and connection

- Use `stui version` and `stui status` to identify this installation and whether
  its background server is reachable. These do not start an orchestration run.
  Use command-specific `--help` for the syntax this binary supports.
- stui is the local, single-machine product. It has no account, relay, remote
  machines, `doc` command or MCP server. Do not substitute `st` or st's MCP tool
  for a failed stui command: that operates a different product and state.
- A shell permission or connection failure does not prove the server or workers
  have exited. Keep the error and report what could not be checked. Use an
  available, authorized terminal for stui when the current shell cannot reach it;
  do not change sandbox permissions or stop a server merely to obtain status.
- OSC 7501 reports an agent's state; it is not a transport for commands, briefs
  or replies. Use the worker commands for those.

## Read the relevant topic

| Work requested | Read |
| --- | --- |
| Sessions, windows, panes, attach, UI configuration or UI extensions | `stui skills get terminal` |
| Task files, briefs, inbox messages and result files | `stui skills get documents` |
| Agent hooks, skill installation, usage and goal diagnostics | `stui skills get hooks` |
| Delegate work, supervise workers or operate a task graph | `stui skills get orchestrate` |
| Carry out a coordinator's task and report back | `stui skills get worker` |

A status check, file handoff or hook diagnosis does not require a new run or new
workers. If coordination is requested, inspect the existing run with
`stui orch status` and reuse the intended run; read the orchestration guide before
starting workers. A label or pane id from an old conversation may now identify
something else: resolve current workers with `stui worker list` before sending.

If `STUI_RUN` is set, or stui tells you a coordinator adopted you, also read the
worker guide. Complete the assigned task within its ownership and report verified
completion with `stui worker done --summary "<result and checks>"`. A lead who is
also a worker reads the coordinator guide when delegating and reports the team's
result to its own coordinator. Keep the user's requested scope; a skill does not
authorize unrelated installs, messages, process termination or delegation.
