// stui-hook.js: written by `stui hooks install` (stui), removed by
// `stui hooks uninstall`. It tells stui when this Pi's turns start and end, so stui
// can show the pane working or idle and orchestration can use it as a
// worker. It only watches: it decides nothing, prints nothing and changes
// nothing in Pi.
//
// Pi has no place to register a command against its events (Claude Code,
// Codex and Gemini CLI do); its extensions are modules. So stui writes this
// one, and it runs `stui hook pi` with each event, under the names stui reads
// for Claude's hooks. The child inherits TMUX_PANE, which is how stui knows
// which pane spoke.
//
// No dependencies: node:child_process only. Edit it and `stui hooks status`
// calls it changed; `stui hooks install` writes it again.

import { spawn } from "node:child_process";

// The version of this file. stui reads it back to tell an older stui's
// extension (outdated) from this one; it is Claude's `stui hook claude --hv 2`,
// which a file has no command line to carry.
const STUI_HOOK_VERSION = __VERSION__;
const STUI_COMMAND = __COMMAND__;

export default function (pi) {
  // A turn stui was told started, so each ends once. Pi's turn is everything
  // from a prompt to agent_settled: retries, compaction and queued messages
  // run more agent runs in between, and agent_end is not the end.
  let busy = false;
  // How the last run ended, from agent_before_settle and agent_end.
  let outcome = "completed";
  let failure = "";
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
        // hook that says Pi ended. The payload goes as the last argument: no
        // pipe to write before Pi exits.
        const child = spawn(STUI_COMMAND, ["hook", "pi", payload], {
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

  const session = (ctx) => {
    try {
      return ctx?.sessionManager?.getSessionId?.() ?? "";
    } catch {
      return "";
    }
  };

  const send = (event, ctx, extra) => {
    const payload = JSON.stringify({
      hook_event_name: event,
      session_id: session(ctx),
      cwd: ctx?.cwd ?? process.cwd(),
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

  // A turn that began without a prompt stui saw (a message an extension
  // sent). Claude's tool hooks are how stui hears of one, so it goes as a tool
  // hook with no tool.
  const start = (ctx) => {
    if (busy) return;
    busy = true;
    outcome = "completed";
    failure = "";
    send("PreToolUse", ctx);
  };

  pi.on("session_start", async (_event, ctx) => {
    busy = false;
    send("SessionStart", ctx);
  });

  pi.on("before_agent_start", async (event, ctx) => {
    if (event?.messageType !== "user" || busy) return;
    busy = true;
    outcome = "completed";
    failure = "";
    send("UserPromptSubmit", ctx);
  });

  pi.on("agent_start", async (_event, ctx) => start(ctx));

  pi.on("tool_execution_start", async (event, ctx) => {
    // A call another tool made (a codemode script's) is part of that one.
    if (event?.parentToolCallId) return;
    start(ctx);
    send("PreToolUse", ctx, { tool_name: event?.toolName ?? "", tool_input: input(event?.args) });
  });

  pi.on("tool_execution_end", async (event, ctx) => {
    if (event?.parentToolCallId) return;
    send("PostToolUse", ctx, { tool_name: event?.toolName ?? "" });
  });

  // Pi asks nothing of its own; an extension that confirms a tool call (or
  // asks anything) blocks its turn on a dialog, and someone is wanted at the
  // pane until it closes. Only inside a turn: a dialog a command opens is
  // the person already there.
  pi.on("ui_prompt_start", async (event, ctx) => {
    if (!busy) return;
    send("PermissionRequest", ctx, { tool_name: event?.title ?? event?.kind ?? "" });
  });

  pi.on("ui_prompt_end", async (_event, ctx) => {
    if (!busy) return;
    send("PermissionResult", ctx);
  });

  pi.on("agent_end", async (event) => {
    const last = [...(event?.messages ?? [])].reverse().find((m) => m?.role === "assistant");
    if (last?.stopReason === "error") failure = String(last.errorMessage ?? "").slice(0, 500);
  });

  pi.on("agent_before_settle", async (event) => {
    if (event?.outcome) outcome = event.outcome;
  });

  pi.on("agent_settled", async (event, ctx) => {
    if (!busy) return;
    busy = false;
    // Esc is a person's choice, not a failure: nobody is told it finished.
    if (event?.aborted || outcome === "aborted") send("Interrupt", ctx);
    else if (outcome === "error") send("StopFailure", ctx, { error: failure, error_type: "error" });
    else send("Stop", ctx);
  });

  pi.on("session_shutdown", async (event, ctx) => {
    // /new, /resume, /fork and reload start another session at once, which
    // says so; only quitting ends the pane's agent.
    if (event?.reason !== "quit") return;
    // Pi is going: what is queued goes first, then the end, briefly.
    await Promise.race([send("SessionEnd", ctx), new Promise((r) => setTimeout(r, 1500))]);
  });
}
