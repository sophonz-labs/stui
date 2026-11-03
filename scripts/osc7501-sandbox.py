#!/usr/bin/env python3
"""A disposable tmux and local server; no login, live state or public tunnel."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def main():
    root = Path(__file__).resolve().parent.parent
    product = 'stui' if (root / 'main.go').exists() else 'st'
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, help='use this binary instead of building the checkout')
    parser.add_argument('--keep', action='store_true', help='keep sandbox alive until Enter or Ctrl-C')
    args = parser.parse_args()
    real_tmux = shutil.which('tmux')
    if not real_tmux:
        raise RuntimeError('tmux 3.2+ is required')
    # A short path also fits macOS Unix socket limits.
    with tempfile.TemporaryDirectory(prefix='osc7501-', dir='/tmp') as directory:
        work = Path(directory)
        sock = work / 'sock'
        state = work / 'state'
        state.mkdir()
        env = {k: v for k, v in os.environ.items()
               if k not in ('TMUX', 'TMUX_PANE') and not k.startswith(('SOPHONZ_', 'STUI_', 'ST_'))}
        env.update({
            'SOPHONZ_HOME': str(state), 'STUI_HOME': str(state),
            'CLAUDE_CONFIG_DIR': str(work / 'claude'), 'CODEX_HOME': str(work / 'codex'),
            'XDG_CONFIG_HOME': str(work / 'config'), 'TMUX_TMPDIR': str(work),
            'OSC7501_TMUX': real_tmux, 'OSC7501_SOCKET': str(sock),
        })
        shimdir = work / 'bin'
        shimdir.mkdir()
        shim = shimdir / 'tmux'
        shim.write_text('#!/bin/sh\nexec "$OSC7501_TMUX" -S "$OSC7501_SOCKET" "$@"\n')
        shim.chmod(0o700)
        env['PATH'] = str(shimdir) + os.pathsep + env.get('PATH', '')
        env['SOPHONZ_TMUX_BIN'] = env['STUI_TMUX_BIN'] = str(shim)
        # Do not use the person's provider logins, keychain, or hooks.
        (state / 'usage').mkdir()
        (state / 'usage' / 'poll.json').write_text('{"off":true}\n')
        (state / 'hooks.json').write_text('{"auto":false}\n')
        Path(env['CLAUDE_CONFIG_DIR']).mkdir()
        Path(env['CODEX_HOME']).mkdir()
        binary = args.binary.resolve() if args.binary else work / product
        if not args.binary:
            subprocess.run(['go', 'build', '-o', str(binary), './cmd/st' if product == 'st' else '.'], cwd=root, env=env, check=True)
            if product == 'st':
                if not (root / 'web/dist/sessions.html').exists():
                    subprocess.run(['sh', 'scripts/build-web.sh'], cwd=root, env=env, check=True)
                env['SOPHONZ_DEV_ROOT'] = str(root)
                env['SOPHONZ_DIST'] = str(root / 'web/dist')
        driver = work / 'driver.sh'
        driver.write_text('stty -echo\nprintf "OSC7501_READY\\n"\nwhile IFS= read -r command; do eval "$command"; done\n')
        server = None
        log = None

        def tmux(*words, check=True):
            return subprocess.run([real_tmux, '-S', str(sock), *words], env=env, text=True,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=check, timeout=5).stdout.strip()

        def until(label, probe, timeout=12):
            deadline = time.monotonic() + timeout
            last = None
            while time.monotonic() < deadline:
                if server is not None and server.poll() is not None:
                    raise RuntimeError('server exited: ' + (work / 'server.log').read_text()[-4000:])
                try:
                    last = probe()
                    if last:
                        return last
                except (OSError, ValueError, urllib.error.URLError):
                    pass
                time.sleep(0.025)
            raise RuntimeError(f'timed out: {label}; last={last!r}')

        try:
            pane = tmux('-f', '/dev/null', 'new-session', '-d', '-P', '-F', '#{pane_id}', '-s', 'osc7501', '-x', '100', '-y', '30', shlex.join(['/bin/sh', str(driver)]))
            until('pane ready', lambda: 'OSC7501_READY' in tmux('capture-pane', '-p', '-t', pane))
            log = (work / 'server.log').open('w')
            if product == 'st':
                # The existing agent reads its API port and token into agent.json.
                with socket.socket() as listener:
                    listener.bind(('127.0.0.1', 0))
                    port = listener.getsockname()[1]
                token = secrets.token_hex(24)
                command = [str(binary), '__agent', '--mux', 'tmux', '--session', 'osc7501', '--port', str(port), '--token', token]
            else:
                command = [str(binary), '__serve']
            server = subprocess.Popen(command, cwd=root, env=env, stdout=log, stderr=log)
            if product == 'st':
                # __agent is the foreground half: normally the CLI writes
                # this record. Keep the same state format in OUR home so
                # --keep can also open st ui without spawning a second agent.
                record = {'pid': server.pid, 'port': port, 'token': token,
                          'session': 'osc7501', 'startedAt': int(time.time() * 1000)}
                record_path = state / 'agent.json'
                record_path.touch(mode=0o600)
                record_path.write_text(json.dumps(record))
            else:
                record = until('private server record', lambda: json.loads((state / 'agent.json').read_text()))

            def snapshot():
                req = urllib.request.Request(f'http://127.0.0.1:{record["port"]}/api/agents',
                                             headers={f'x-{product if product == "stui" else "sophonz"}-token': record['token']})
                with urllib.request.urlopen(req, timeout=2) as response:
                    return json.load(response)

            def current():
                return next((p for p in snapshot().get('panes', []) if p['id'] == pane), None)

            until('private API', snapshot)
            until('control client', lambda: 'control-mode' in tmux('list-clients', '-F', '#{client_flags}'))

            def send(command):
                tmux('send-keys', '-t', pane, '-l', command)
                tmux('send-keys', '-t', pane, 'Enter')

            def expect(label, command, state_name, report_state, **fields):
                send(command)
                def matches():
                    p = current()
                    rep = (p or {}).get('report') or {}
                    return p if p and p.get('reported') and p.get('source') == 'report' and p['state'] == state_name and rep.get('state') == report_state and all(rep.get(k) == v for k, v in fields.items()) else None
                p = until(label, matches)
                print(label + ': ' + json.dumps({k: p.get(k) for k in ('state', 'reported', 'report')}, sort_keys=True), flush=True)

            expect('working ST', r"printf '\033]7501;state=working:app=sandbox:progress=40\033\\'", 'working', 'working', app='sandbox', progress=40)
            expect('blocked BEL', r"printf '\033]7501;state=blocked:kind=permission\007'", 'waiting', 'blocked', kind='permission')
            expect('done', r"printf '\033]7501;state=done\007'", 'done', 'done')
            expect('error', r"printf '\033]7501;state=error\007'", 'error', 'error')
            expect('malformed pair skipped', r"printf '\033]7501;broken:state=working:app=sandbox\007'", 'working', 'working', app='sandbox')
            # Two printf writes, with a file barrier proving the first finished.
            barrier = work / 'partial-written'
            send(r"printf '\033]7501;state=blo'; " + shlex.join(['touch', str(barrier)]))
            until('first write', barrier.exists)
            expect('split report', r"printf 'cked:kind=permission\033\\'", 'waiting', 'blocked', kind='permission')
            send(r"printf '\033]7501;state=clear\007'; printf '\033]9;needs input\007Proceed? [y/n] '")
            def cleared():
                p = current()
                return p if p and not p.get('reported') and not p.get('report') and p.get('source') == 'screen' and p.get('prompt') else None
            p = until('clear restores screen', cleared)
            print('clear restores screen: ' + json.dumps({k: p.get(k) for k in ('state', 'reported', 'report', 'source')}, sort_keys=True), flush=True)
            screen = tmux('capture-pane', '-p', '-t', pane)
            if '7501;' in screen or '\x1b]' in screen:
                raise RuntimeError('unknown OSC leaked into visible pane text')
            print('tmux visible text: OSC stripped, prompt intact (tmui itself is not launched)', flush=True)
            if args.keep:
                expect('inspect working', r"printf '\033]7501;state=working:app=sandbox:progress=40\007'", 'working', 'working', app='sandbox', progress=40)
                # An executable environment file for commands against ONLY this sandbox.
                runner = work / 'run'
                runner.write_text('#!/bin/sh\nexec env -u TMUX -u TMUX_PANE -u SOPHONZ_URL -u SOPHONZ_TOKEN ' + ' '.join(shlex.quote(k + '=' + v) for k, v in env.items() if k in ('PATH', 'SOPHONZ_HOME', 'STUI_HOME', 'OSC7501_TMUX', 'OSC7501_SOCKET', 'CLAUDE_CONFIG_DIR', 'CODEX_HOME', 'XDG_CONFIG_HOME', 'TMUX_TMPDIR', 'SOPHONZ_TMUX_BIN', 'STUI_TMUX_BIN')) + ' "$@"\n')
                runner.chmod(0o700)
                print('Inspect: ' + shlex.join([str(runner), str(binary), 'ui', 'agents']))
                print('Attach: ' + shlex.join([str(runner), 'tmux', 'attach-session', '-t', 'osc7501']))
                print('tmui:   ' + shlex.join([str(runner), 'tmui']))
                input('Sandbox stays alive; Enter or Ctrl-C cleans it up: ')
        finally:
            if server is not None:
                server.terminate()
                try:
                    server.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait()
            tmux('kill-server', check=False)
            if log is not None:
                log.close()


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as exc:
        print(f'osc7501 sandbox: {exc}', file=sys.stderr)
        if isinstance(exc, subprocess.CalledProcessError) and exc.stderr:
            print(exc.stderr.strip(), file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        sys.exit(130)
