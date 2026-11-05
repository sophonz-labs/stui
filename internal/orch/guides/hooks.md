# stui hooks and coding-agent skills

Use this topic when agent state, usage or installed instructions are missing or
outdated. Draft 2026-11-06.

`stui hooks status` inspects the agent hooks. Hooks report agent activity;
`stui plugin status` inspects the Claude Code plugin and Codex skills that teach
agents to use stui. `stui ext list` inspects a third system: UI extensions.
Installing one does not establish that either of the others works.

When hook installation or repair is requested, use `stui hooks install <agent>`
and read the receipt (`stui hooks --help` lists the interface). opencode hooks
are included; that does not mean `stui plugin install` installs an opencode skill.
Restart the affected agent as its hook installer instructs. Check a real turn's
state afterward: files being present alone does not prove events are delivered.

`stui plugin install` installs the common skill entrypoint and worker entrypoint
for Claude Code and Codex. The existing names `stui-orchestrate`, `stui-worker`
and `/stui:orchestrate` remain. Both entrypoints read this binary's own guides;
new agent sessions see newly installed skills. Re-running the installer repairs
stale templates and refreshes an already registered Claude plugin. Server startup
also refreshes previously installed skills, including Codex-only installations;
it does not opt a new agent into skills. Errors are reported rather than treated
as a successful update.

Use `stui usage` and `stui usage --help` for usage readings and polling status.
Claude's status-line feed is configured with `stui hooks install --usage`;
missing data is not zero usage. `stui goal --help` describes Claude's goal loop
and `stui usage guard --help` its usage guard. Do not enable a loop or change
polling/hook settings just to inspect a reading. OSC 7501 state reports do not
replace usage collection or the worker message channel.
