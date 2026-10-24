#!/usr/bin/env python3
"""Write the trusted Cloudflare proxy ranges from its public, unauthenticated API."""

import argparse
import ipaddress
import json
import os
from pathlib import Path
import tempfile
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        '--output', type=Path,
        default=Path('/opt/1panel/www/sites/unour/cloudflare-realip.conf'),
    )
    args = parser.parse_args()
    url = 'https://api.cloudflare.com/client/v4/ips'
    request = urllib.request.Request(url, headers={'User-Agent': 'UNOur-Deployment/1.0'})
    with urllib.request.urlopen(request, timeout=30) as response:
        payload = json.load(response)
    if payload.get('success') is not True:
        raise ValueError('Cloudflare did not return a successful IP range response')
    networks = []
    for version, key in ((4, 'ipv4_cidrs'), (6, 'ipv6_cidrs')):
        entries = payload['result'][key]
        if not isinstance(entries, list) or not entries:
            raise ValueError(f'Missing Cloudflare IPv{version} ranges')
        for entry in entries:
            network = ipaddress.ip_network(entry, strict=True)
            if network.version != version:
                raise ValueError(f'Incorrect address family: {entry}')
            networks.append(network)
    lines = [f'# Source: {url}']
    lines += [f'set_real_ip_from {network};' for network in networks]
    lines += ['real_ip_header CF-Connecting-IP;', 'real_ip_recursive on;', '']
    # Validate everything before replacing an existing, working include.
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', dir=args.output.parent, delete=False) as target:
            temporary = Path(target.name)
            target.write('\n'.join(lines))
        temporary.chmod(0o644)
        os.replace(temporary, args.output)
        temporary = None
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
    print(f'Wrote {len(networks)} validated proxy ranges to {args.output}')


if __name__ == '__main__':
    main()
