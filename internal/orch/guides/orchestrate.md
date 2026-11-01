# stui orchestration: the coordinator's guide

You are coordinating other coding agents through stui. Each worker is a real
Claude Code or Codex session in its own tmux window of this session; the user
can watch every one of them (`stui`, the orch view). You are the coordinator:
you split the work, start workers, wait for them, check what they did, and
report back. You do not do a worker's task yourself while it runs.
`stui --skill` prints this guide again in one read. Revised 2026-11-02, rev 9.

## The loop

```bash
stui orch start --objective "<the user's goal in one line>"
stui worker start --agent claude --name <short-name> --task <file>   # or --task - (stdin)
stui worker start --agent codex  --name <short-name> --task <file>
stui worker start --agent opencode --name <short-name> --task <file>
stui worker start --agent pi       --name <short-name> --task <file>
stui worker wait --check-in 3m          # all workers; --any for the first
stui worker read <id>                   # a worker's last answer and its summary
stui worker send <id> "<follow-up>"     # more work, or an answer to its question (one line)
stui worker send <id> --file <file>     # more than a line: write it to a file first
stui worker list                        # your workers and theirs as a tree: ids, names, states
stui worker inbox                       # what was owed to you: news stui could not type in
```

A send that cannot be typed now (someone is typing in that pane, a question
is up) is not lost: `stui worker send` says so and leaves it in the worker's
inbox, and stui types `[stui] N messages for you -- stui worker inbox` there
between its turns. The same line in your pane is your own inbox: a
worker's news whose line was not seen to go in.

A worker may ask you something and wait: `[stui] <worker> (%12) asks: … --
answer: stui worker reply <id> "<answer>"`. Answer with that command; the
worker gets it at once. If only a person can decide, ask them first, then
reply.

Every command takes `--json`. A worker is its tmux pane id (`%12`) or the
name `stui worker list` shows (`api-tests`). A name is looked for among your
workers and theirs first, then every worker on this machine. A name two workers share does
nothing and lists them: use the id.
A name is a label, not where the worker is: an adopted agent's name comes
from its tmux window title, which the agent or the person set. Give a worker
the name of what it does with `stui worker rename <id> <name>`; stui refuses a
name another worker has.
Pane ids change when tmux is restarted or its sessions restored, and an old
id can then belong to another agent. `stui worker send` and `stop` refuse a
pane that is not one of your run's and say your workers' ids now; take the
id from that, or from `stui worker list`, rather than from memory. `--help` on
any command only shows its help.

- Start the whole independent wave first, then wait. Prefer parallel waves to
  chains deeper than three steps. Workers can start workers (two levels below
  you at most; stui refuses deeper). A team adopted as your worker counts from
  its own lead, and the whole chain above and below is at most four long.
- `stui worker start` returns only once the worker has begun its turn (stui sees
  its hooks). A non-zero exit says which stage failed and what was left
  (a window you may close with `stui worker stop <id> --yes`). Do not retry
  blindly: read the receipt.
- A worker's state: `working`, `waiting` (a question), `done` (it ran `stui
  worker done`), `idle` (its turn ended without that: read it, it may have
  stopped to ask in its answer), `exited`.
- You need not poll. When a worker is done, or ends its turn without `stui
  worker done`, stui types one line into your pane between your turns (never
  over text in your input box): `[stui] worker ux (%31) is done: <summary> --
  stui worker read %31`. A worker below a worker that is already done comes up
  to you the same way ("under mobile"). Read it and go on.
  `stui orch history` lists what was handed to whom, when, whether it went in,
  and who was told.
- "Ended its turn without stui worker done" (state `idle`) is not done. The
  worker stopped, and `stui worker read <id>` says why when stui knows (its
  usage limit, an error, an interrupt). Otherwise its answer does: it may ask
  you something there, or say it finished and forgot to report. Answer it
  with `stui worker send`. If it says the work is finished, check it against
  the acceptance criteria yourself, or `stui worker send <id> "report with stui
  worker done"`. Do not give its task to another worker meanwhile: the work
  is still in that one.
- An instruction longer than a line goes in a file: `stui worker send <id>
  --file <file>` (or `--file -` from a pipe). Do not paste a long text in
  quotes: it can burst the worker's input box, and the shell runs what is in
  `backticks` inside double quotes before stui sees it. stui keeps an unchanging
  copy of the file, and types the worker one line saying where the copy is.
  Do not put secrets in such a file: stui keeps it as it is.
- `stui worker wait` exits 0 when the workers are done or idle, 3 when one needs input
  (`waiting`: read it, then `stui worker send` the answer or tell the user), and
  2 on timeout. A timeout is a checkpoint, not a failure: check `stui worker
  list` and wait again.
- Prefer `--check-in 3m`: the wait comes back after three minutes, leaving the
  workers running, with each one's state, how long it has been so and its
  title (exit 2, `checkedIn` in `--json`). Wait again, or look at the one
  that worries you (`stui worker read`). Without it a wait holds until the
  workers are done or `--timeout` (15m), and `--for done` alone can hold on
  a worker that ended its turn without reporting. Never treat silence as done; never stop or restart a
  worker without positive proof it stopped (its pane gone, or it said done).
- Before you report, read every worker's result and check it against the
  task's acceptance criteria (run the tests it names).

## When the work is over

Once you have read and checked a worker's result and will ask it nothing
more:

- Keep what it made: `stui worker merge <id>` (a fan: `stui worker pick <id>`),
  then `stui worker clean <id>` removes its worktree and branch.
- Close its window: `stui worker stop <id> --yes`. An adopted worker was the
  user's session first: `stui worker release <id>` lets it go and its pane
  stays.
- `stui worker list` shows what is left. Then report to the user: what was
  done, and how it was checked.

## Tasks with an order

When part of the work can start only after another part is done, give stui the
order instead of keeping it in your head:

```bash
stui task create schema --task schema.md --agent codex --owns "db/**"
stui task create api    --task api.md    --agent claude --deps schema --owns "internal/api/**"
stui task create ui     --task ui.md     --agent claude --deps api,schema --owns "web/**"
stui task list                     # waiting / ready / running / done / blocked
stui task start schema             # refused while a --deps task is not done
stui task wait --timeout 15m       # 0: one is ready (or all done), 3: a worker asks,
                                 # 4: nothing can move (blocked), 2: timed out
```

Loop: `stui task wait`, then `stui task start` every ready task, until all are
done. A task whose worker reports `blocked:` holds back what depends on it:
read it, fix the cause (another task, or `stui worker send`), or tell the user.

## Owned paths

Workers that edit the same folder at once overwrite each other. Give each one
the paths it may change: `--owns` on `stui worker start` or `stui task create`
(comma separated, relative to its folder; `*`, `**`, a directory owns what is
under it), or `stui worker own <id> "src/auth/**"` for one already running.
stui refuses a Claude worker's edit outside them before it happens and tells it
why; it then asks you in its answer. Files outside the folder are not held
back. Codex workers and shell commands are not checked yet: say the ownership
in their task too.

## A worktree per worker

`--worktree` (on `stui worker start` or `stui task create`) gives the worker its
own worktree and branch (`stui/<run>/<name>`, from the commit this checkout is
on), in a folder outside the repository: its files, builds and tests never
mix with anyone else's. Files git does not keep that a worktree needs
(`.env.local`) are copied in when `.worktreeinclude` names them.

A folder that is not a git repository works too: stui snapshots it into a
repository of its own in stui's home (nothing is written into the folder) and
the worktree starts from that snapshot. What a `.gitignore` there leaves out,
and dependencies and build output (`node_modules/`, `target/`, `dist/`, ...),
are not copied. A folder of more than 20,000 files or 2 GiB is refused with
the numbers; start in a smaller folder or add `--any-size`.

A worktree has its own id, `<run>/<worker>`, which still names it after its
worker's pane is gone.

```bash
stui worker trees                     # every worktree, its id, and what it is from
stui worker diff <id|tree> [--stat]   # what it changed, committed or not, and from what
stui worker ask <id|tree> "why X?"    # a reading agent in the worktree answers; never the worker
stui worker merge <id|tree> [--commit]  # its branch into this checkout; --commit commits what it left
stui worker clean <id|tree>           # remove the worktree and branch once merged
```

`stui worker ask` opens a window in the worktree with an agent of its own
(the worker's agent, or `--agent`), read-only where stui knows how (Claude in
plan mode, Codex with a read-only sandbox), and the worktree's changes in its
first prompt. It is not a worker: nothing is typed into the worker, so a turn
in progress goes on. Ask the same worktree again and it is the same window.

A merge that conflicts merges nothing: stui aborts it and names the files. Send
the worker to bring its branch up to date and resolve them (`stui worker send
<id> "merge main into your branch, resolve <files>, commit"`), then merge
again. Outside git, use `--owns` instead.

## Several tries at once (a fan)

When the right answer is not obvious (a design direction, a hard bug), give the
same task to several workers and pick the best result:

```bash
stui worker start --fan 3 --agent claude,codex --task fix.md   # each in its own worktree
stui worker fan                       # side by side: what each changed, done or not
stui worker pick <id>                 # merge that one; the others' windows and worktrees go
```

Read each one's summary and diff (`stui worker diff <id>`) before you pick.

## Decisions only a person can make

When you need the user to choose (a design direction, something risky,
something you were told to ask about), open a decision gate and stop:

```bash
stui gate ask "Which image strategy?" --title "Images" \
  --option "A: keep them as attachments" --option "B: OCR to text" [--text]
```

The user sees it in `stui gate list` and answers with `stui gate answer`. When they
answer, stui types it into your pane as your
next message ("Decision gate <id> answered: …"). Until then, stop your turn;
`stui gate list` shows what is open. Workers may open gates too; `stui task wait`
and `stui worker wait` see a worker on a gate as asking.

`stui gate list` keeps the newest 200. Ask a decision through a gate, not in
your text, and write the question so it makes sense to someone who has not
read your conversation.

## The task file

Each worker gets a file. Make it self-contained:

- **Target**: the files, component or environment in scope.
- **Change**: the concrete result to produce.
- **Constraints**: what must not change; compatibility rules.
- **Ownership**: what this worker may edit; what other workers own.
- **Acceptance**: the test, command or evidence that proves it is done.

stui appends the worker's obligations (report with `stui worker done`).

## Adopted workers

An agent that was already running in another pane (any session) can become
your worker with its conversation intact: you run `stui worker adopt <id>
--role "<why you bring it in>"`. stui tells you its id when that happens.

Before you give it anything, know what it is. `stui worker adopt` prints what
stui knows at once: whether it leads a team (and its members), whether it is
working and for how long, the last thing it was asked, and where it works.
It is also asked for a brief of what it has done, once its own turn ends, so
asking does not interrupt it: `stui worker wait <id> --brief` waits for that,
and `stui worker read <id>` shows it under "brief". An agent in the middle of
something is not a free worker: do not push a new task over work it is doing
for someone; read its brief, and give work that fits.

It has no task file: give it work with `stui worker send <id> "<task, with the
acceptance criteria>"`, then `stui worker wait <id>` and `stui worker read <id>`
as for any worker. It knows what it learned before, so say only what is new.
Its role shows under its name in `stui worker list`; change it with `stui worker
about <id> "<role>"`. `stui worker stop` does not close an adopted worker (it
was the user's session first); `stui worker release <id>` lets it go.

## A team as one worker

An agent that directs workers of its own (a coordinator and its team) can be
adopted too. It stays its team's lead and keeps its run; to you it is one
worker, and `stui worker list` shows its team under it (`--depth 1`: "team N"
on its row). Give work to the lead
only, never to its team members directly: the lead splits it among its team,
collects their results and reports once with `stui worker done --summary`, so
`stui worker wait <lead>` is all you wait on. Releasing the lead leaves its team
with it.
