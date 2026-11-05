# Orchestration

One coding agent, the coordinator, splits a task and hands the parts to other agents, the workers. Each worker is a real Claude Code, Codex, opencode or Pi session in its own tmux window of the coordinator's session, so you can watch or attach to any of them. stui types the tasks in, tells the coordinator when a worker is done, and keeps a record of what went where. Revised 2026-11-06, rev 1.

The agents run these commands themselves. The full guides they read are built into stui:

```sh
stui skills                   # common entrypoint; adds obligations for STUI_RUN workers
stui skills get common        # choose the topic for this task
stui skills get orchestrate   # the coordinator's guide
stui skills get worker        # the worker's guide
stui plugin install           # Claude Code plugin and Codex skills that load them
```

## One entrypoint, topics as needed

The existing `stui-orchestrate` skill (Claude Code: `/stui:orchestrate`) now
covers stui operations even when no delegation is requested. It reads the
common guide, which routes to `terminal`, `documents`, `hooks`, `orchestrate`
or `worker`. `stui skills get all` prints all six, common first. Status checks,
brief delivery and hook diagnosis do not require a new run or workers.

`stui-worker` remains the automatic worker entrypoint: it reads common and
worker instructions, including the completion report. Adopted workers also
follow their coordinator's brief even without `STUI_RUN`. Existing explicit
`stui skills get orchestrate` and `stui skills get worker` commands still work.

These are stui guides: they do not require st or expose remote machines, an st
vault/document API, or an MCP server. The documents topic covers task files,
briefs, inbox messages and workspace result files. Hooks and UI extensions
remain distinct from coding-agent skills.

`stui plugin install` repairs the templates and refreshes an already installed
Claude plugin. A release binary's background server also repairs previously installed skills
at startup, including Codex-only installs and stale templates at the same
version. It does not install skills for a new agent. A failed Claude cache
update is logged and retried on a later server startup. New agent sessions
load the refreshed skills. Development builds do not automatically replace
installed skills. The entrypoints get detailed guides from the binary,
so the commands match the installed stui version.

## A run

```sh
stui orch start --objective "make the parser tests pass"
stui worker start --agent claude --name tests --task task.md
stui worker wait --check-in 3m      # 0: done or idle, 3: one asks, 2: timeout or check-in
stui worker read tests              # its last answer and its summary
stui worker send tests --file followup.md
stui worker list
stui orch history
```

`stui worker start` opens a window, starts the agent, types the task (a pointer to the file) and returns once the agent's hooks say its turn began. The worker reports with:

```sh
stui worker done --summary "<what it did, and how it checked>"
```

stui then types one line into the coordinator's pane between its turns: `[stui] worker tests (%5) is done: … -- stui worker read %5`. A message that cannot be typed (someone is typing there, a prompt is up) waits in that pane's inbox (`stui worker inbox`).

A recorded run is in [captures/orchestration.txt](captures/orchestration.txt).

## More than one worker

- `--worktree` gives a worker its own git worktree and branch (`stui/<run>/<name>`). `stui worker diff`, `merge` and `clean` work on it.
- `--owns "src/x/**"` limits what a Claude worker may edit; stui refuses edits outside it through the hook.
- `--fan 3 --agent claude,codex` gives one task to several workers; `stui worker fan` compares them and `stui worker pick` keeps one.
- `stui task create … --deps a,b` orders tasks; `stui task wait` and `stui task start` run them as they become ready.
- `stui worker adopt <pane>` makes an agent that is already running a worker, conversation intact.
- `stui gate ask "<question>" --option A --option B` asks a person; `stui gate answer` answers and stui types the answer into the agent.

## Where things are kept

Under `~/.stui/orch/<run>/`: the run, the briefs sent with `--file`, each worker's task.

## Limits

- One machine. There are no remote workers.
- A worker is told it is one through its environment (`STUI_RUN`, `STUI_TASK_FILE`); its hooks must be installed (`stui hooks install`) for stui to see its turns start and end.
- A run is tied to its panes' ids. After tmux restarts, the ids change and stui does not yet rebind a run to the new panes.
- Owned paths are enforced for Claude Code only; say them in other agents' tasks too.
