#!/usr/bin/env python3
"""A stand-in for Claude Code at a permission prompt, for the agent watch
tests on a real tmux: it draws a transcript and the prompt, and redraws the
way Ink does -- a resize clears the screen and prints the whole transcript
again before the prompt comes back; a re-render erases the prompt's lines and
writes them again. Never reads input except to exit on "q"."""

import os
import shutil
import signal
import sys
import time

TRANSCRIPT = int(os.environ.get("FAKE_TRANSCRIPT", "120"))
# Seconds the transcript takes to come out again after a resize.
REDRAW = float(os.environ.get("FAKE_REDRAW", "1.4"))
# Seconds between re-renders of the prompt (0: never).
RERENDER = float(os.environ.get("FAKE_RERENDER", "3"))
QUESTION = os.environ.get(
    "FAKE_QUESTION",
    "Do you want to proceed?",
)
COMMAND = os.environ.get(
    "FAKE_COMMAND",
    "rm -rf build/ && make -j8 all && ./scripts/run-integration-tests.sh --suite agent-watch --verbose",
)

out = sys.stdout


def w(s):
    out.write(s)
    out.flush()


def wrap(text, width, indent, more=None):
    more = indent if more is None else more
    words, lines, cur = text.split(" "), [], ""
    for word in words:
        ind = more if lines else indent
        cand = (cur + " " + word) if cur else word
        if len(cand) + ind > width and cur:
            lines.append(" " * ind + cur)
            cur = word
        else:
            cur = cand
    if cur:
        lines.append(" " * (more if lines else indent) + cur)
    return lines


def prompt_lines():
    cols = shutil.get_terminal_size((120, 40)).columns
    ls = ["─" * cols, " Bash command", ""]
    ls += wrap(COMMAND, cols, 3)
    ls += ["   Build and run the integration tests", ""]
    ls += wrap(QUESTION, cols, 1)
    ls += [" ❯ 1. Yes"]
    ls += wrap("2. Yes, and don't ask again for rm and make commands in /private/tmp/aw/project-with-a-long-name", cols, 3, 6)
    ls += ["   3. No, and tell Claude what to do differently (esc)", "", " Esc to cancel · Tab to amend"]
    return ls


drawn = 0


def draw_prompt():
    global drawn
    ls = prompt_lines()
    w("\r\n".join(ls))
    drawn = len(ls)


def full_redraw():
    w("\x1b[2J\x1b[3J\x1b[H")
    w(" ▐▛███▜▌   Claude Code v2.1.283\r\n\r\n")
    step = REDRAW / max(TRANSCRIPT, 1)
    for i in range(TRANSCRIPT):
        w("  ⎿  line %d of what the agent printed before it asked\r\n" % i)
        time.sleep(step)
    draw_prompt()


def rerender():
    # Up to the first line of the prompt, erase below, write it again.
    if drawn > 1:
        w("\x1b[%dA\r\x1b[J" % (drawn - 1))
    time.sleep(0.02)
    draw_prompt()


pending = {"resize": False}


def on_winch(*_):
    pending["resize"] = True


signal.signal(signal.SIGWINCH, on_winch)
full_redraw()
last = time.time()
while True:
    time.sleep(0.05)
    if pending["resize"]:
        pending["resize"] = False
        full_redraw()
        last = time.time()
    elif RERENDER and time.time() - last > RERENDER:
        rerender()
        last = time.time()
