#!/usr/bin/python3
"""Restricted SSH receiver for CI-built UNOur images. No remote shell commands."""

import argparse
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


@dataclass(frozen=True)
class Release:
    sha: str
    run: int
    attempt: int
    digest: str

    @property
    def tag(self):
        # Retrying a commit can rebuild mutable base packages differently.
        return f'{self.sha}-{self.run}-{self.attempt}'


def parse_command(command):
    fields = shlex.split(command)
    if (len(fields) != 5 or fields[0] != 'deploy'
            or not re.fullmatch(r'[0-9a-f]{40}', fields[1])
            or not re.fullmatch(r'[1-9][0-9]{0,14}', fields[2])
            or not re.fullmatch(r'[1-9][0-9]{0,5}', fields[3])
            or not re.fullmatch(r'[0-9a-f]{64}', fields[4])):
        raise ValueError('Expected: deploy <commit-sha> <run-number> <attempt> <archive-sha256>')
    return Release(fields[1], int(fields[2]), int(fields[3]), fields[4])


def superseded(release, previous):
    if not previous:
        return False
    if release.run == previous['run'] and release.sha != previous['sha']:
        raise ValueError('A workflow run cannot change its commit SHA')
    return (release.run, release.attempt) <= (previous['run'], previous['attempt'])


def with_releases(contents, versions):
    updates = {f'UNOUR_{component.upper()}_RELEASE': value for component, value in versions.items()}
    lines = []
    seen = set()
    for line in contents.splitlines():
        key = line.partition('=')[0].strip()
        if key in updates:
            if key not in seen:
                lines.append(f'{key}={updates[key]}')
                seen.add(key)
        else:
            lines.append(line)
    lines.extend(f'{key}={value}' for key, value in updates.items() if key not in seen)
    return '\n'.join(lines) + '\n'


def atomic_write(path, contents):
    path = Path(path)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, delete=False) as stream:
            temporary = Path(stream.name)
            stream.write(contents)
            stream.flush()
            os.fsync(stream.fileno())
        temporary.chmod(0o600)
        os.replace(temporary, path)
        temporary = None
        descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def receive_image(stream, path, expected, maximum):
    digest = hashlib.sha256()
    total = 0
    try:
        with path.open('xb') as target:
            while chunk := stream.read(1024 * 1024):
                total += len(chunk)
                if total > maximum:
                    raise ValueError('Image upload exceeds the configured size limit')
                digest.update(chunk)
                target.write(chunk)
        if total == 0 or digest.hexdigest() != expected:
            raise ValueError('Image upload failed SHA256 verification')
    except BaseException:
        path.unlink(missing_ok=True)
        raise


class Deployer:
    def __init__(self, config):
        self.repo = Path(config['repo_dir'])
        self.releases = Path(config['releases_dir'])
        self.backups = Path(config['backup_dir'])
        self.project = config.get('project', 'unour')
        self.health_url = config.get('health_url', 'http://127.0.0.1:18080/healthz')
        self.lock = Path(config.get('lock_file', '/run/lock/unour-deploy.lock'))
        self.maximum = config.get('max_image_bytes', 256 * 1024 * 1024)
        self.wait_seconds = config.get('wait_seconds', 60)

    def compose(self, *arguments):
        subprocess.run(['docker', 'compose', '-p', self.project, *arguments], cwd=self.repo, check=True)

    def current_version(self, component):
        image = subprocess.check_output(
            ['docker', 'inspect', '--format', '{{.Config.Image}}', f'{self.project}-{component}-1'],
            text=True,
        ).strip()
        prefix = f'unour-{component}:'
        if not image.startswith(prefix) or not re.fullmatch(r'[A-Za-z0-9_.-]+', image[len(prefix):]):
            raise ValueError(f'Unexpected running image for {component}: {image}')
        return image[len(prefix):]

    def wait_healthy(self):
        deadline = time.monotonic() + 30
        while True:
            try:
                with urllib.request.urlopen(self.health_url, timeout=3) as response:
                    if response.status == 200 and json.load(response).get('status') == 'ok':
                        return
            except (OSError, ValueError, urllib.error.HTTPError):
                pass
            if time.monotonic() >= deadline:
                raise RuntimeError('Application health check failed')
            time.sleep(1)

    def activate(self, component):
        self.compose('up', '-d', '--no-build', '--no-deps', '--wait',
                     '--wait-timeout', str(self.wait_seconds), component)
        self.wait_healthy()

    def deploy(self, component, release, stream):
        self.releases.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix=f'.upload-{component}-', dir=self.releases) as directory:
            archive = Path(directory) / 'image.tar.gz'
            receive_image(stream, archive, release.digest, self.maximum)
            # The lock covers both components, even when their workflows overlap.
            with self.lock.open('a') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                state_path = self.releases / f'.deploy-state-{component}.json'
                state = json.loads(state_path.read_text()) if state_path.exists() else None
                if superseded(release, state):
                    print(f'Skipped superseded {component} workflow run {release.run}.', flush=True)
                    return
                subprocess.run(['docker', 'image', 'load', '--input', str(archive)], check=True)
                image = f'unour-{component}:{release.tag}'
                user = subprocess.check_output(
                    ['docker', 'image', 'inspect', '--format', '{{.Config.User}}', image], text=True,
                ).strip()
                expected_user = 'unour:unour' if component == 'api' else 'nginx'
                if user != expected_user:
                    raise ValueError(f'{image} must run as {expected_user}')
                previous = {service: self.current_version(service) for service in ('api', 'web')}
                environment = self.repo / '.env'
                # Reconcile metadata with actual containers after interrupted/manual releases.
                original = with_releases(environment.read_text(), previous)
                atomic_write(environment, original)
                subprocess.run(
                    ['sh', str(self.repo / 'deploy/backup.sh'), str(self.backups)],
                    cwd=self.repo, check=True, env=dict(os.environ, COMPOSE_PROJECT_NAME=self.project),
                )
                try:
                    atomic_write(environment, with_releases(original, {component: release.tag}))
                    self.activate(component)
                except BaseException:
                    atomic_write(environment, original)
                    print(f'Deployment failed; restoring {component}:{previous[component]}.', flush=True)
                    self.activate(component)
                    raise
                record = asdict(release) | {
                    'component': component,
                    'image': image,
                    'previous_image': f'unour-{component}:{previous[component]}',
                    'deployed_at': datetime.now(timezone.utc).isoformat(),
                }
                atomic_write(state_path, json.dumps(record, indent=2) + '\n')
                record_path = self.releases / f'ci-{component}-{release.sha}-{release.run}-{release.attempt}.json'
                atomic_write(record_path, json.dumps(record, indent=2) + '\n')
                print(f'Deployed {image}; previous image retained.', flush=True)


def interrupted(signum, _frame):
    signal.signal(signum, signal.SIG_IGN)
    raise RuntimeError(f'Deployment interrupted by signal {signum}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('component', choices=('api', 'web'))
    parser.add_argument('--config', type=Path, default=Path('/etc/unour/deploy.json'))
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise PermissionError('The SSH receiver must run as root with a restricted forced command')
    config = json.loads(args.config.read_text())
    command = os.environ.get('SSH_ORIGINAL_COMMAND', '')
    if command == 'check':
        print(f'UNOur restricted deployment receiver ready: {args.component}')
        return
    release = parse_command(command)
    for signum in (signal.SIGTERM, signal.SIGHUP):
        signal.signal(signum, interrupted)
    Deployer(config).deploy(args.component, release, sys.stdin.buffer)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(f'UNOur deployment error: {error}', file=sys.stderr)
        sys.exit(1)
