#!/bin/sh
set -eu

site_dir=${UNOUR_SITE_DIR:-/opt/1panel/www/sites/unour}
certificate="$site_dir/letsencrypt/live/csac.chat/fullchain.pem"
state_file="$site_dir/letsencrypt/.unour-loaded-sha256"
exec 9>/run/lock/unour-certificate.lock
flock -n 9 || exit 0
if [ ! -s "$certificate" ]; then
    printf 'Certificate missing: complete the initial HTTP-01 issuance in docs/deployment.md first.\n' >&2
    exit 1
fi

renew_status=0
docker run --rm --name unour-certbot \
    --volume "$site_dir/acme:/var/www/acme" \
    --volume "$site_dir/letsencrypt:/etc/letsencrypt" \
    certbot/certbot:v5.8.0 renew --quiet "$@" || renew_status=$?

fingerprint=$(sha256sum "$certificate")
fingerprint=${fingerprint%% *}
loaded=''
if [ -f "$state_file" ]; then
    loaded=$(cat "$state_file")
fi
# A failed reload leaves the fingerprint unacknowledged so the next run retries,
# including when Certbot has no further certificate to issue that day.
if [ "$loaded" != "$fingerprint" ]; then
    container=${OPENRESTY_CONTAINER:-}
    if [ -z "$container" ]; then
        set -- $(docker ps -q --filter label=com.docker.compose.service=openresty)
        if [ "$#" -ne 1 ]; then
            printf 'Expected one running OpenResty container; set OPENRESTY_CONTAINER.\n' >&2
            exit 1
        fi
        container=$1
    fi
    docker exec "$container" nginx -t
    docker exec "$container" nginx -s reload
    temporary=$(mktemp "$state_file.XXXXXX")
    trap 'rm -f "$temporary"' 0
    printf '%s\n' "$fingerprint" > "$temporary"
    chmod 0600 "$temporary"
    mv -f "$temporary" "$state_file"
fi
exit "$renew_status"
