# Hook fixtures

Paths, prompts and task descriptions are redacted in all of them. Draft 2026-10-03.

These are built from the hooks reference (https://code.claude.com/docs/en/hooks)
and the Claude Code 2.1.288 CLI's own code, not captured from a run (agent
teams need a signed-in interactive session): `claude-PreToolUse-Agent`,
`claude-PostToolUse-Agent-async`, `claude-SubagentStart-teammate` (an
in-process teammate's id is `<name>@<team>`), `claude-TeammateIdle`,
`claude-TaskCreated`, `claude-TaskCompleted`.
