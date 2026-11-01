# stui orchestration: the worker's guide

Another coding agent (the coordinator) started you through stui and gave you a
task file (its path is in `$STUI_TASK_FILE`). The user can watch you.
`stui --skill` prints this guide again in one read. Revised 2026-11-02, rev 7.

- Do the task in the file, within its ownership and constraints. Do not edit
  what it says other workers own. If stui refuses an edit ("stui: this worker may
  change only …"), do not work around it: say in your answer what you needed
  to change and why, and stop for the coordinator.
- If you need a decision you cannot make from the task, ask your coordinator
  and wait for the answer: `stui worker question "<question>"` (it returns the
  answer; exit 2 means none came in time, and `stui worker question --resume
  <id>` waits again). Or ask in your answer and stop: the coordinator reads
  it (`stui worker read`) and answers you. If only a
  person can decide (the task says so, or it is risky), open a gate instead:
  `stui gate ask "<question>" --option A --option B`, then stop; the answer
  comes as your next message. Write the question to stand on its own:
  the person may not have read your conversation.
- With a worktree of your own (`stui worker start --worktree`), commit your work
  on your branch before you report done; the coordinator merges it.
- When you are done, check the acceptance criteria, then report in one line:

```bash
stui worker done --summary "<what you did, and how you checked it>"
```

- If you cannot finish, say why in the summary, starting with `blocked:`.
- Ending your turn without `stui worker done` is not a report: stui tells your
  coordinator you "ended your turn without stui worker done", and it has to
  read your answer to learn why. Stop that way only to ask something in your
  answer. When the work is finished, run `stui worker done`.
- stui reads Claude Code and Codex from their hooks and screens. Another agent
  can tell stui its state with OSC 7501, written to the pane's terminal (a
  tool's shell has none of its own): `printf '\033]7501;state=working:app=<name>\033\\' > "$(tmux display -p -t "$TMUX_PANE" '#{pane_tty}')"`,
  with `working`, `blocked`, `done`, `error` or `idle`. Once you report, stui
  believes only your reports: report every change, and `state=clear` to hand
  it back.
- A message that says `A message from … is in <file> (…). Read it and do
  it.` is your next instruction: read that file and do it, as you did your
  task file. Words before ` -- ` are a note that goes with it.
- A line `[stui] N messages for you -- stui worker inbox` means something sent to
  you could not be typed into your pane when it came. Run `stui worker inbox`:
  it shows them (and marks them read); then do them as you would any message.
- You may start workers of your own for independent parts (`stui worker start`),
  one level deep at most. When one is done, stui types a line into your pane
  (`[stui] worker … is done: … -- stui worker read <id>`): read it, then go on or
  report. Ending your turn while your own workers work is waiting for them,
  not stopping short.

## When you were adopted

If stui told you that you are now a worker of another pane, you were running
before and keep everything you know. You have no task file: the tasks come as
messages from that agent. Do each as above and report each with `stui worker
done --summary`. Until a task comes, carry on with what the user asked you.

stui asks you once, between your turns, for a brief: what you have done so far
and what you are doing now, in five lines or fewer (your team's work too, if
you lead one). It is not a new task. Leave it and carry on:

```bash
stui worker brief --summary "<the five lines>"
```

It is not your report: `stui worker done --summary` still says when a task is
done.

## When you lead a team

If you had workers of your own when you were adopted, they stay yours: you
are your team's coordinator and one worker to the agent above you. When a
task comes, hand parts to your team as before (`stui worker send`, `stui worker
start`), wait for them, check their results, and report once for the whole
team: `stui worker done --summary "<what the team did, and how it was
checked>"`. Do not report before your team is done. The agent above you gives
work to you, not to your team; if it asks a member directly, take it as a
task for you.
