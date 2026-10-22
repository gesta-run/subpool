#!/usr/bin/env python3
"""Run an explicit one-shot job using the existing deployment's credentials."""
import json
import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import urlsplit, urlunsplit

root = Path(__file__).resolve().parent
container = json.loads(subprocess.check_output(
    ['docker', 'inspect', 'subpool-pre-subpool-1'], text=True))[0]
environment = dict(value.split('=', 1) for value in container['Config']['Env'] if '=' in value)
database = json.loads(subprocess.check_output(
    ['docker', 'inspect', 'subpool-pre-postgres-1'], text=True))[0]
networks = container['NetworkSettings']['Networks']
network = next(name for name in networks if name in database['NetworkSettings']['Networks'])
database_ip = database['NetworkSettings']['Networks'][network]['IPAddress']
url = urlsplit(environment['SUBPOOL_DATABASE_URL'])
userinfo = url.netloc.rsplit('@', 1)[0]
environment['SUBPOOL_DATABASE_URL'] = urlunsplit((
    url.scheme, userinfo + '@' + database_ip + ':' + str(url.port or 5432),
    url.path, url.query, url.fragment))
os.execve(str(root / 'subpool-reset-once'), [str(root / 'subpool-reset-once'),
    '--job', str(root / 'job.json'), '--state', str(root / 'state.json'), *sys.argv[1:]],
    {**os.environ, **environment})
