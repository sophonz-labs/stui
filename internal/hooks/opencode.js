// stui-hook.js: written by `stui hooks install` (stui), removed by
// `stui hooks uninstall`. It tells stui when this opencode's turns start and end,
// so stui can show the pane working or idle and orchestration can use it as a
// worker. It only watches: it decides nothing, prints nothing and changes
// nothing in opencode.
//
// opencode has no place to register a command against its events (Claude Code,
// Codex and Gemini CLI do); its plugins are modules. So stui writes this module,
// and the module runs `stui hook opencode` with each event, as the others do. The
// child inherits TMUX_PANE, which is how stui knows which pane spoke.
//
// No dependencies: node:child_process only. Edit it and `stui hooks status`
// calls it changed; `stui hooks install` writes it again.

import { spawn } from "node:child_process";

// The version of this file. stui reads it back to tell an older stui's plugin
// (outdated) from this one; it is Claude's `stui hook claude --hv 2`, which a
// file has no command line to carry.
const STUI_HOOK_VERSION = __VERSION__;
const STUI_COMMAND = __COMMAND__;

export const StHook = async ({ directory }) => {
  // Sessions that are a subagent's (the task tool): their turns are part of
  // their parent's, and their idle comes in the middle of it.
  const children = new Set();
  // Root sessions with a turn stui was told started, so each turn ends once:
  // opencode says session.idle twice after an interrupt.
  const busy = new Set();
  // One stui hook at a time, in order, as an agent that waits on its hooks
  // runs them; nothing here ever waits on it.
  let chain = Promise.resolve();

  const run = (payload) =>
    new Promise((resolve) => {
      let done = false;
      const finish = () => {
        if (!done) {
          done = true;
          clearTimeout(timer);
          resolve();
        }
      };
      const timer = setTimeout(finish, 3000);
      try {
        // Its own process group, so Ctrl-C in the pane does not stop the
        // hook that says opencode ended. The payload goes as the last
        // argument: no pipe to write before opencode exits.
        const child = spawn(STUI_COMMAND, ["hook", "opencode", payload], {
          stdio: "ignore",
          detached: true,
          windowsHide: true,
        });
        child.on("error", finish);
        child.on("exit", finish);
        child.unref();
      } catch {
        finish();
      }
    });

  const send = (event, sessionID, extra) => {
    const payload = JSON.stringify({
      hook_event_name: event,
      session_id: sessionID || "",
      cwd: directory,
      ...extra,
    });
    chain = chain.then(() => run(payload));
    return chain;
  };

  // What a tool was given, cut short: stui keeps a one-line summary of it.
  const input = (v) => {
    try {
      const s = JSON.stringify(v ?? {});
      return s.length > 4000 ? { description: s.slice(0, 4000) } : v;
    } catch {
      return {};
    }
  };

  const start = (sessionID) => {
    if (busy.has(sessionID)) return false;
    busy.add(sessionID);
    return true;
  };

  return {
    "chat.message": async ({ sessionID }) => {
      if (!sessionID || children.has(sessionID)) return;
      busy.add(sessionID);
      send("UserPromptSubmit", sessionID);
    },
    "tool.execute.before": async ({ tool, sessionID }, output) => {
      if (!sessionID || children.has(sessionID)) return;
      start(sessionID);
      send("PreToolUse", sessionID, { tool_name: tool, tool_input: input(output?.args) });
    },
    "tool.execute.after": async ({ tool, sessionID }) => {
      if (!sessionID || children.has(sessionID)) return;
      send("PostToolUse", sessionID, { tool_name: tool });
    },
    event: async ({ event }) => {
      const p = event?.properties ?? {};
      switch (event?.type) {
        case "session.created": {
          const info = p.info ?? {};
          if (info.parentID) children.add(info.id);
          else send("SessionStart", info.id ?? p.sessionID);
          return;
        }
        case "session.deleted":
          children.delete(p.info?.id);
          busy.delete(p.info?.id);
          return;
        case "session.status":
          // A turn that began without a prompt stui saw. Claude's tool hooks
          // are how stui hears of one, so it goes as a tool hook with no tool.
          // Only the change to busy: opencode says busy on every step.
          if (p.status?.type !== "busy" || !p.sessionID || children.has(p.sessionID)) return;
          if (start(p.sessionID)) send("PreToolUse", p.sessionID);
          return;
        case "session.idle":
          if (!p.sessionID || children.has(p.sessionID) || !busy.delete(p.sessionID)) return;
          send("Stop", p.sessionID);
          return;
        case "session.error": {
          // An interrupt (Esc twice) is an error to opencode and a person's
          // choice to stui. Either way the session.idle after it is the same
          // turn's end, which this one has said already.
          if (!p.sessionID || children.has(p.sessionID) || !busy.delete(p.sessionID)) return;
          const name = p.error?.name ?? "";
          if (name === "MessageAbortedError") send("Interrupt", p.sessionID);
          else send("StopFailure", p.sessionID, { error: String(p.error?.data?.message ?? name).slice(0, 500), error_type: name });
          return;
        }
        case "permission.asked":
          // A subagent's ask too: either way someone is wanted at this pane.
          send("PermissionRequest", p.sessionID, { tool_name: p.permission, tool_input: input(p.metadata) });
          return;
      }
    },
    dispose: async () => {
      // opencode is going: what is queued goes first, then the end, briefly.
      await Promise.race([send("SessionEnd", ""), new Promise((r) => setTimeout(r, 1500))]);
    },
  };
};
