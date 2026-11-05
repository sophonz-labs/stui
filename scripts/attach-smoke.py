#!/usr/bin/env python3
"""Real PTY attach/input/resize/detach checks; requires Python's pexpect package."""
import argparse
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import tempfile
import time
import traceback

import pexpect


def run(args):
    binary = args.binary.resolve()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='attach-', dir='/tmp') as tmp:
        root = Path(tmp)
        env = {k: v for k, v in os.environ.items()
               if not k.startswith(('TMUX', 'TMUI_', 'STUI_', 'ST_', 'SOPHONZ_'))}
        env.update(HOME=str(root / 'home'), XDG_CACHE_HOME=str(root / 'cache'),
                   XDG_CONFIG_HOME=str(root / 'config'), STUI_HOME=str(root / 'state'),
                   SOPHONZ_HOME=str(root / 'unused-st'), TMUX_TMPDIR=str(root),
                   SHELL='/bin/bash', TERM=args.term, LC_ALL='C.UTF-8',
                   PS1='ATTACH_READY> ', PROMPT_COMMAND='')
        for name in ('home', 'cache', 'config', 'state', 'state/usage'):
            (root / name).mkdir(parents=True, exist_ok=True)
        (root / 'state/hooks.json').write_text('{"auto":false}\n')
        (root / 'state/usage/poll.json').write_text('{"off":true}\n')
        (root / 'home/.bashrc').write_text("PS1='ATTACH_READY> '\nunset PROMPT_COMMAND\n")
        (root / 'home/.bash_profile').write_text('. ~/.bashrc\n')
        for product in ('tmui', 'stui'):
            conf = root / 'config' / product
            conf.mkdir()
            (conf / 'config.yaml').write_text('refreshRate: 500ms\nplugins:\n  enabled: false\n')
        # All product tmux calls use this private default server, including the
        # stui watcher. No live socket, user config, provider credentials or hooks.
        program = [str(binary)] + (['--embedded-tmux'] if args.embedded else [])
        tmux_command = program + ['tmux']
        child = server = None
        server_log = None
        transcript = (output / 'terminal.log').open('w')
        events = (output / 'events.log').open('w')

        def event(message):
            print(message, flush=True)
            events.write(message + '\n')
            events.flush()

        def tmux(*words, check=True):
            p = subprocess.run(tmux_command + list(words), env=env, cwd=output,
                               capture_output=True, text=True, timeout=10)
            if check and p.returncode:
                raise RuntimeError(f'tmux {words}: {p.stderr or p.stdout}')
            return p.stdout.strip()

        def wait(label, probe, timeout=20):
            end = time.monotonic() + timeout
            while time.monotonic() < end:
                if probe():
                    return
                if server is not None and server.poll() is not None:
                    raise RuntimeError('stui server exited; see server.log')
                time.sleep(0.05)
            raise RuntimeError('timed out: ' + label)

        def expect(text):
            # The actual bytes read from the PTY are logged, not sent input.
            child.expect_exact(text, timeout=20)

        def send(text):
            child.send(text)

        def spawn(command):
            return pexpect.spawn(command[0], command[1:], env=env, cwd=str(output),
                                 encoding='utf-8', codec_errors='replace',
                                 dimensions=(32, 120), timeout=20)

        try:
            metadata = {'platform': platform.platform(), 'mode': args.mode, 'term': args.term,
                        'tmux': tmux('-V'), 'product': args.product, 'rounds': args.rounds,
                        'binary': binary.name}
            (output / 'metadata.json').write_text(json.dumps(metadata, indent=2) + '\n')
            event(json.dumps(metadata))
            tmux('-vv', '-f', '/dev/null', 'new-session', '-d', '-s', 'zzz-seed',
                 '-x', '120', '-y', '32')
            tmux('set-option', '-g', 'default-shell', '/bin/bash')
            tmux('set-option', '-g', 'default-command', 'exec /bin/bash --noprofile --rcfile ' + shlex.quote(str(root / 'home/.bashrc')) + ' -i')
            if args.product == 'stui' and args.mode != 'direct':
                server_log = (output / 'server.log').open('w')
                server = subprocess.Popen(program + ['__serve'], env=env, cwd=root,
                                          stdout=server_log, stderr=server_log)
                wait('stui server record', lambda: (root / 'state/agent.json').exists())
            if args.mode == 'direct':
                tmux('new-session', '-d', '-s', 'aaa-target')
                tmux('new-window', '-t', '=aaa-target:', '-n', 'input')
                child = spawn(tmux_command + ['attach-session', '-t', '=aaa-target'])
            else:
                # Record the UI's own tty settings around execution, including
                # when it runs inside an outer tmux pane.
                cmd = program + (['ui'] if args.product == 'stui' else [])
                wrapper = root / 'ui.sh'
                wrapper.write_text('stty -g > ' + shlex.quote(str(root / 'before')) + '\n' +
                                   shlex.join(cmd) + '\nresult=$?\n' +
                                   'stty -g > ' + shlex.quote(str(root / 'after')) + '\n' +
                                   'printf "%s\\n" "$result" > ' + shlex.quote(str(root / 'exit')) + '\n' +
                                   ('exec sleep 300\n' if args.mode == 'popup' else 'exit "$result"\n'))
                if args.mode == 'popup':
                    tmux('new-session', '-d', '-s', 'zzz-ui', '-x', '120', '-y', '32',
                         shlex.join(['/bin/sh', str(wrapper)]))
                    child = spawn(tmux_command + ['attach-session', '-t', '=zzz-ui'])
                else:
                    child = spawn(['/bin/sh', str(wrapper)])
            child.logfile_read = transcript
            if args.mode != 'direct':
                expect('Sessions')
                send('n')
                expect('New session name')
                send('aaa-target\r')
                wait('UI creates session', lambda: 'aaa-target' in tmux('list-sessions', '-F', '#{session_name}'))
                # Filter to the new session; wait for its refreshed row first.
                expect('aaa-target')
                send(':windows aaa-target\r')
                expect('Windows')
                send('n')
                expect('New window')
                send(('input' if args.product == 'tmui' else '') + '\r')
                wait('UI creates window', lambda: len(tmux('list-windows', '-t', '=aaa-target', '-F', '#{window_id}').splitlines()) == 2)
                # Attach from the windows view using the normal user key.
                send('a')
            expect('ATTACH_READY> ')
            event('session/window created and attached')
            for turn in range(args.rounds):
                for n in range(10):
                    marker = f'ECHO_{turn}_{n}'
                    # Before Enter, the shell cannot have executed anything:
                    # seeing this proves that typed characters are visible.
                    send('# ' + marker)
                    expect(marker)
                    send('\r')
                    expect('ATTACH_READY> ')
                    send(f"printf 'EXEC_%s\\n' '{turn}_{n}'\r")
                    expect(f'EXEC_{turn}_{n}')
                    expect('ATTACH_READY> ')
                    if n == 4:
                        child.setwinsize(28 + turn, 100 + turn)
                    time.sleep(args.interval)
                tmux('list-sessions')  # a crashed server fails here, never skips
                event(f'round {turn + 1}: visible input, execution, resize, server alive')
                send('\x02d')
                if args.mode == 'direct':
                    child.expect(pexpect.EOF)
                    child.close()
                    if child.exitstatus != 0:
                        raise RuntimeError(f'tmux attach exit: {child.exitstatus}/{child.signalstatus}')
                    if turn + 1 < args.rounds:
                        child = spawn(tmux_command + ['attach-session', '-t', '=aaa-target'])
                        child.logfile_read = transcript
                        expect('ATTACH_READY> ')
                else:
                    expect('Windows')
                    if turn + 1 < args.rounds:
                        send('a')
                        expect('ATTACH_READY> ')
            if args.mode != 'direct':
                # A command prompt must still render typed text after detach.
                send(':sessions')
                expect('sessions')
                send('\r')
                expect('Sessions')
                send('\x03')
                wait('UI exit and tty restore', lambda: (root / 'exit').exists())
                if (root / 'exit').read_text().strip() != '0':
                    raise RuntimeError('UI did not exit successfully')
                before = (root / 'before').read_text()
                after = (root / 'after').read_text()
                (output / 'tty-state.txt').write_text(f'before: {before}after: {after}')
                if before != after:
                    raise RuntimeError('UI left tty settings changed (echo/raw mode)')
                if args.mode == 'popup':
                    send('\x02d')
                child.expect(pexpect.EOF)
                child.close()
                if child.exitstatus != 0:
                    raise RuntimeError('terminal client exited abnormally')
            tmux('list-sessions')
            event('PASS: attach/input/detach and tty restoration')
        finally:
            # Capture before cleanup. Do not archive agent.json or its API token.
            for name, words in [('sessions', ['list-sessions']), ('clients', ['list-clients']),
                                ('panes', ['list-panes', '-a', '-F', '#{session_name} #{pane_id} #{pane_dead} #{pane_current_command}'])]:
                try:
                    (output / (name + '.txt')).write_text(tmux(*words, check=False) + '\n')
                except (OSError, subprocess.TimeoutExpired):
                    pass
            if child is not None and child.isalive():
                child.close(force=True)
            if server is not None:
                server.terminate()
                try:
                    server.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait()
            tmux('kill-server', check=False)
            if server_log:
                server_log.close()
            transcript.close()
            events.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--product', choices=['tmui', 'stui'], required=True)
    parser.add_argument('--mode', choices=['direct', 'outside', 'popup'], required=True)
    parser.add_argument('--embedded', action='store_true')
    parser.add_argument('--term', default='xterm-256color')
    parser.add_argument('--rounds', type=int, default=3)
    parser.add_argument('--interval', type=float, default=0.5)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if not 1 <= args.rounds <= 50 or not 0 <= args.interval <= 5:
        parser.error('rounds must be 1..50 and interval 0..5 seconds')
    try:
        run(args)
    except Exception:
        args.output.mkdir(parents=True, exist_ok=True)
        (args.output / 'failure.txt').write_text(traceback.format_exc())
        raise


if __name__ == '__main__':
    main()
