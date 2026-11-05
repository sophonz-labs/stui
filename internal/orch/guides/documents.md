# stui task files and briefs

Use files to hand work and results between agents on this machine. stui has no
`doc` command, vault or document-id API. Draft 2026-11-06.

- Put a new worker's task in a file and pass it with `stui worker start --task
  <file>`. Starting a worker requires a request to delegate; a file alone does
  not require a new worker or run.
- For an existing worker, use `stui worker send <id> --file <file>`. Write the
  brief before sending. Do not put a multiline brief in a quoted shell argument:
  shell substitutions can change it before stui reads it.
- Identify the worker with `stui worker list` and read its current result with
  `stui worker read <id>`. A brief should give the goal, measured facts, owned
  files, constraints, acceptance checks and what to report back.
- Inspect the send receipt. A queued or refused message is not confirmed
  delivery. `stui worker inbox` reads messages owed to the current pane.
  A message pointing to a saved brief means read that file in the assigned
  task's scope; it is not a reason to start a second orchestration run.
- stui keeps a copy of briefs sent with `--file`. Use the saved path reported
  by stui rather than guessing a storage path. Do not send credentials in a
  brief. Files on another machine are not available through stui.
- Keep requested result documents in the agreed workspace and include their
  paths in the verified completion summary. Do not promise an st document id.
