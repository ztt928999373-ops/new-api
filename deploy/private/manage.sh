#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077
export LC_ALL=C

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo=$(git -C "$here" rev-parse --show-toplevel)
revision=6132c5d8cb5d95e28a6444af4def7da780c2776f
image="suanjie-api:sha-$revision"
source_url=https://github.com/ztt928999373-ops/new-api
unset DOCKER_CONTEXT DOCKER_HOST DOCKER_TLS_VERIFY DOCKER_CERT_PATH
unset PRIVATE_PROJECT PRIVATE_POSTGRES_IMAGE PRIVATE_PORT PRIVATE_DB_ADMIN_PASSWORD PRIVATE_DB_PASSWORD PRIVATE_SESSION_SECRET PRIVATE_CRYPTO_SECRET
unset COMPOSE_PROFILES COMPOSE_REMOVE_ORPHANS COMPOSE_ENV_FILES
export COMPOSE_DISABLE_ENV_FILE=1

die() { echo "$*" >&2; exit 1; }
docker_local() { docker --host unix:///var/run/docker.sock "$@"; }

check_docker() {
    [[ $(uname -s):$(uname -m) == Linux:x86_64 ]] || die 'Linux amd64 required.'
    local engine compose_version
    engine=$(docker_local version --format '{{.Server.Version}}')
    compose_version=$(docker_local compose version --short)
    compose_version=${compose_version#v}
    [[ $engine =~ ^([0-9]+)\. ]] && (( BASH_REMATCH[1] >= 28 )) || die 'Docker Engine 28 or later required.'
    [[ $compose_version =~ ^([0-9]+)\.([0-9]+)\. ]] && (( BASH_REMATCH[1] > 2 || (BASH_REMATCH[1] == 2 && BASH_REMATCH[2] >= 24) )) || die 'Compose 2.24.4 or later required.'
    if [[ $compose_version =~ ^2\.24\.([0-9]+) ]] && (( BASH_REMATCH[1] < 4 )); then
        die 'Compose 2.24.4 or later required.'
    fi
}

verify_image() {
    local ref=$1 meta file expected actual
    meta=$(docker_local image inspect --format '{{.Os}}/{{.Architecture}} {{index .Config.Labels "org.opencontainers.image.revision"}} {{index .Config.Labels "org.opencontainers.image.source"}} {{index .Config.Labels "org.opencontainers.image.licenses"}} {{json .Config.Entrypoint}}' "$ref")
    [[ $meta == "linux/amd64 $revision $source_url AGPL-3.0-only [\"/new-api\"]" ]] || die 'Application image identity does not match the approved source.'
    for file in LICENSE NOTICE THIRD-PARTY-LICENSES.md; do
        expected=$(git -C "$repo" cat-file blob "$revision:$file" | sha256sum)
        actual=$(docker_local run --rm --pull never --platform linux/amd64 --network none --read-only --cap-drop ALL --entrypoint sha256sum "$ref" "/licenses/$file")
        [[ ${actual%% *} == "${expected%% *}" ]] || die "Image license checksum mismatch: $file"
    done
}

validate_pg() {
    [[ $1 =~ ^(docker\.io/library/)?postgres:17\.(0|[1-9][0-9]{0,2})-bookworm@sha256:[0-9a-f]{64}$ ]] || die 'Provide an official postgres:17.MINOR-bookworm@sha256:<verified digest> reference.'
}

validate_port() {
    [[ $1 =~ ^[1-9][0-9]{3,4}$ ]] && (( $1 >= 1024 && $1 <= 65535 )) || die 'Invalid local port.'
}

new_state() {
    local mode=$1 pg=$2 port=$3 name value project_suffix
    validate_pg "$pg"
    validate_port "$port"
    project_suffix=$(openssl rand -hex 8)
    mkdir -m 700 -- "$state" || die 'State directory must be new.'
    {
        printf 'PRIVATE_PROJECT=suanjie-%s-%s\n' "$mode" "$project_suffix"
        printf 'PRIVATE_POSTGRES_IMAGE=%s\nPRIVATE_PORT=%s\n' "$pg" "$port"
        for name in DB_ADMIN_PASSWORD DB_PASSWORD SESSION_SECRET CRYPTO_SECRET; do
            value=$(openssl rand -hex 32)
            [[ $value =~ ^[0-9a-f]{64}$ ]] || die 'Could not generate a secret.'
            printf 'PRIVATE_%s=%s\n' "$name" "$value"
        done
    } > "$state/.env"
}

load_state() {
    local envfile=$1 line key val secret
    local -A entries=() seen_secrets=()

    [[ -f $envfile && ! -L $envfile && -O $envfile && $(stat -c %a "$envfile") == 600 ]] || die 'Private .env must be an owner-only regular file (0600).'
    while IFS= read -r line || [[ -n $line ]]; do
        [[ $line =~ ^(PRIVATE_[A-Z_]+)=([0-9a-zA-Z./:@-]+)$ ]] || die 'Invalid private state format.'
        key=${BASH_REMATCH[1]} val=${BASH_REMATCH[2]}
        [[ ! -v entries[$key] ]] || die 'Duplicate private state field.'
        entries[$key]=$val
    done < "$envfile"
    [[ ${#entries[@]} == 7 ]] || die 'Incomplete private state.'
    project=${entries[PRIVATE_PROJECT]:-}
    pg_image=${entries[PRIVATE_POSTGRES_IMAGE]:-}
    [[ $project =~ ^suanjie-(private|smoke)-[0-9a-f]{16}$ ]] || die 'Unexpected project name.'
    validate_pg "$pg_image"
    validate_port "${entries[PRIVATE_PORT]:-}"
    for key in DB_ADMIN_PASSWORD DB_PASSWORD SESSION_SECRET CRYPTO_SECRET; do
        secret=${entries[PRIVATE_$key]:-}
        [[ $secret =~ ^[0-9a-f]{64}$ && ! -v seen_secrets[$secret] ]] || die 'Invalid or reused secret in private state.'
        seen_secrets[$secret]=1
    done
}

compose() {
    docker_local compose --env-file "$state/.env" -p "$project" -f "$here/compose.yml" "${overlay[@]}" "$@"
}

check_app_tag() {
    [[ $(docker_local image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image") == "$revision" ]] || die 'Application image not prepared for this revision.'
}

validate_plan() {
    local mode=$1 path permissions
    local -a assets=(init-db.sql)
    if [[ $mode == smoke ]]; then assets+=(smoke.mjs); fi
    for path in "${assets[@]}"; do
        [[ -f $here/$path && ! -L $here/$path ]] || die "Missing or linked container asset: $path"
        permissions=$(stat -c %a "$here/$path")
        (( 8#$permissions & 4 )) || die "Non-secret container asset must be readable by its container user: $path"
    done
    # Render only into the local verifier; never print the interpolated plan.
    compose config --format json | jq -e --arg mode "$mode" '
        .networks.database.internal == true and
        .networks.database.driver_opts["com.docker.network.bridge.gateway_mode_ipv4"] == "isolated" and
        (.services.db.networks | keys) == ["database"] and
        (.services.db.ports // []) == [] and
        .services.app.environment.REDIS_CONN_STRING == "" and
        .services.app.environment.BATCH_UPDATE_ENABLED == "false" and
        .services.app.environment.SESSION_COOKIE_SECURE == "false" and
        (if $mode == "smoke" then
            (.services | keys) == ["app", "db", "mock"] and
            (.services.app.networks | keys) == ["database"] and
            (.services.mock.networks | keys) == ["database"] and
            (.services.app.ports // []) == [] and (.services.mock.ports // []) == []
        else
            (.services | keys) == ["app", "db"] and
            (.services.app.networks | keys) == ["database", "edge"] and
            (.services.app.ports | length) == 1 and
            .services.app.ports[0].host_ip == "127.0.0.1" and .services.app.ports[0].target == 3000
        end)
    ' >/dev/null || die 'Rendered Compose plan violates the private network or configuration contract.'
}

check_pg17() {
    local version minor=${pg_image#*postgres:17.}
    version=$(compose exec -T db sh -eu -c 'export PGPASSWORD="$SUANJIE_DB_PASSWORD"; exec psql -X -w -h 127.0.0.1 -U suanjie -d suanjie -Atqc "SHOW server_version_num"')
    minor=${minor%%-*}
    [[ $version =~ ^17[0-9]{4}$ ]] && (( 10#$version == 170000 + 10#$minor )) || die 'Running PostgreSQL version does not match the pinned tag.'
    echo "PG_SERVER_VERSION_NUM=$version"
}

require_fresh_project() {
    local containers volumes networks
    containers=$(docker_local ps -aq --filter "label=com.docker.compose.project=$project")
    volumes=$(docker_local volume ls -q --filter "label=com.docker.compose.project=$project")
    networks=$(docker_local network ls -q --filter "label=com.docker.compose.project=$project")
    [[ -z $containers && -z $volumes && -z $networks ]] || die 'Project already has Docker resources.'
}

action=${1:-}
case $action in
    build|import)
        check_docker
        [[ $(git -C "$repo" rev-parse --verify "$revision^{commit}") == "$revision" ]] || die 'Required source object not found.'
        if [[ $action == build ]]; then
            [[ $# == 1 ]] || die 'Usage: manage.sh build'
            if ! docker_local image inspect "$image" >/dev/null 2>&1; then
                # Git archive excludes dirty files, private state and the deployment branch itself.
                git -C "$repo" archive --format=tar "$revision" |
                    docker_local buildx build --platform linux/amd64 --load --provenance=false \
                        --label "org.opencontainers.image.revision=$revision" \
                        --label "org.opencontainers.image.version=sha-$revision" \
                        --label "org.opencontainers.image.source=$source_url" \
                        --label 'org.opencontainers.image.licenses=AGPL-3.0-only' -t "$image" -
            fi
        else
            [[ $# == 2 && $2 =~ ^ghcr\.io/ztt928999373-ops/suanjie-api@sha256:[a-f0-9]{64}$ ]] || die 'Provide the approved D2 GHCR digest, not a tag.'
            docker_local pull --platform linux/amd64 "$2"
            verify_image "$2"
            if docker_local image inspect "$image" >/dev/null 2>&1; then
                [[ $(docker_local image inspect --format '{{.Id}}' "$image") == "$(docker_local image inspect --format '{{.Id}}' "$2")" ]] || die 'Existing local tag is a different image; do not overwrite it.'
            else
                docker_local image tag "$2" "$image"
            fi
        fi
        verify_image "$image"
        docker_local image inspect --format 'APP_IMAGE_ID={{.Id}}' "$image"
        exit 0 ;;
    init|smoke|start|status|stop|stop-app|restart|backup|restore) ;;
    *) die 'Usage: manage.sh build | import GHCR_DIGEST | init STATE PG_DIGEST [PORT] | smoke NEW_STATE PG_DIGEST | start/status/stop/stop-app/restart STATE | backup STATE | restore NEW_STATE TRUSTED_BACKUP [PORT]' ;;
esac

[[ ${2:-} == /* ]] || die 'An absolute private state path is required.'
state=$(realpath -m -- "$2")
[[ $state != / && $state != "$repo" && $state != "$repo"/* ]] || die 'Keep private state outside the source tree.'
check_docker
overlay=()
case $action in
    init|smoke)
        [[ $# -ge 3 && $# -le 4 ]] || die 'Supply new state path and approved PostgreSQL reference.'
        mode=private
        [[ $action != smoke ]] || mode=smoke
        new_state "$mode" "$3" "${4:-3000}"
        ;;
    restore)
        [[ $# -ge 3 && $# -le 4 && $3 == /* ]] || die 'Provide an absolute trusted backup path and optional port.'
        backup=$(realpath -e -- "$3")
        [[ -d $backup && -O $backup && $(stat -c %a "$backup") == 700 ]] || die 'Backup must be in an owner-only directory.'
        [[ ! -e $state && ! -L $state ]] || die 'Restore requires a new state directory.'
        load_state "$backup/secrets.env"
        [[ $project == suanjie-private-* ]] || die 'Not a private deployment backup.'
        validate_port "${4:-3000}"
        verify_image "$image"
        expected_id=$(docker_local image inspect --format '{{.Id}}' "$image")
        for file in database.dump app-data.tar secrets.env versions.txt SHA256SUMS; do
            [[ -f $backup/$file && ! -L $backup/$file && -O $backup/$file && $(stat -c %a "$backup/$file") == 600 ]] || die 'Backup file is missing or not owner-only.'
        done
        [[ $(cat "$backup/versions.txt") == "$(printf 'SOURCE_REVISION=%s\nPOSTGRES_IMAGE=%s\nAPP_IMAGE_ID=%s\n' "$revision" "$pg_image" "$expected_id")" ]] || die 'Backup requires its exact original app image and database reference.'
        # Recompute only the fixed files, rather than opening arbitrary paths from a manifest.
        [[ $(cd -- "$backup"; sha256sum database.dump app-data.tar secrets.env versions.txt) == "$(cat "$backup/SHA256SUMS")" ]] || die 'Backup checksums do not match.'
        suffix=$(openssl rand -hex 8)
        mkdir -m 700 -- "$state"
        sed -e "s/^PRIVATE_PROJECT=.*/PRIVATE_PROJECT=suanjie-private-$suffix/" \
            -e "s/^PRIVATE_PORT=.*/PRIVATE_PORT=${4:-3000}/" "$backup/secrets.env" > "$state/.env"
        ;;
    *) [[ $# == 2 ]] || die 'Expected only the private state path.' ;;
esac
[[ -d $state && ! -L $state && -O $state && $(stat -c %a "$state") == 700 ]] || die 'State must be an owner-only directory (0700).'
exec 9< "$state"
flock -n 9 || die 'Another operation is using this state.'
load_state "$state/.env"

if [[ $action == smoke ]]; then
    overlay=(-f "$here/compose.smoke.yml")
    verify_image "$image"
    validate_plan smoke
    require_fresh_project
    compose pull db mock
    cleanup_smoke() {
        local rc=$?
        trap - EXIT
        if ! compose down --volumes; then
            echo "Smoke cleanup failed for $project; inspect only this synthetic project." >&2
            (( rc != 0 )) || rc=1
        fi
        if (( rc == 0 )); then echo 'SMOKE=PASS (synthetic PostgreSQL 17 only)'; fi
        exit "$rc"
    }
    trap cleanup_smoke EXIT
    compose up -d --no-build --pull never --wait --wait-timeout 120 db
    check_pg17
    compose up -d --no-build --pull never --wait --wait-timeout 180
    app_id=$(compose ps -q app)
    db_id=$(compose ps -q db)
    mock_id=$(compose ps -q mock)
    for id in "$app_id" "$db_id" "$mock_id"; do
        [[ -n $id && $(docker_local inspect --format '{{len .HostConfig.PortBindings}}|{{len .NetworkSettings.Networks}}' "$id") == '0|1' ]] || die 'Smoke container unexpectedly has ports or multiple networks.'
    done
    [[ $(docker_local network inspect --format '{{.Internal}}' "${project}_database") == true ]] || die 'Smoke network is not internal.'
    compose exec -T mock bun /fixture/smoke.mjs verify
    exit 0
fi

[[ $project == suanjie-private-* ]] || die 'Refusing to run private-instance operations on smoke state.'
case $action in
    init) echo 'Private state created; no database or app started.' ;;
    start)
        verify_image "$image"
        validate_plan private
        compose pull db
        compose up -d --no-build --pull never --wait --wait-timeout 120 db
        check_pg17
        compose up -d --no-build --pull never --wait --wait-timeout 180
        ;;
    status) compose ps ;;
    stop) compose stop ;;
    stop-app) compose stop app ;;
    restart)
        check_app_tag
        id=$(compose ps --all -q app)
        [[ -n $id && $(docker_local inspect --format '{{.Image}}' "$id") == "$(docker_local image inspect --format '{{.Id}}' "$image")" ]] || die 'Existing app container does not match the approved local tag.'
        compose restart app
        # Wait without replacing services or applying new configuration.
        for ((i=0; i<36; i++)); do
            id=$(compose ps -q app)
            if [[ -n $id && $(docker_local inspect --format '{{.State.Health.Status}}' "$id") == healthy ]]; then exit 0; fi
            sleep 5
        done
        die 'Application did not become healthy after restart.'
        ;;
    backup)
        check_app_tag
        running=$(compose ps --status running -q app)
        [[ -z $running ]] || die 'Stop app before backup; it will not be restarted automatically.'
        app_id=$(compose ps --all -q app)
        [[ -n $app_id ]] || die 'An existing stopped app container is required.'
        app_image_id=$(docker_local inspect --format '{{.Image}}' "$app_id")
        [[ $app_image_id == "$(docker_local image inspect --format '{{.Id}}' "$image")" ]] || die 'Stopped app and approved local tag differ.'
        check_pg17
        backup=$(mktemp -d "$state/backup-XXXXXXXXXX")
        compose exec -T db sh -eu -c 'export PGPASSWORD="$SUANJIE_DB_PASSWORD"; exec pg_dump -w -h 127.0.0.1 -U suanjie -d suanjie --format=custom --no-owner --no-acl' > "$backup/database.dump"
        compose exec -T db pg_restore --list < "$backup/database.dump" >/dev/null
        compose run --no-deps --rm -T --entrypoint tar app -C /data -cf - . > "$backup/app-data.tar"
        tar -tf "$backup/app-data.tar" >/dev/null
        cp -- "$state/.env" "$backup/secrets.env"
        printf 'SOURCE_REVISION=%s\nPOSTGRES_IMAGE=%s\nAPP_IMAGE_ID=%s\n' "$revision" "$pg_image" "$app_image_id" > "$backup/versions.txt"
        ( cd -- "$backup"; sha256sum database.dump app-data.tar secrets.env versions.txt > SHA256SUMS )
        echo "Backup complete: $backup (contains secrets; encrypt before transfer)"
        ;;
    restore)
        require_fresh_project
        validate_plan private
        compose pull db
        compose up -d --no-build --pull never --wait --wait-timeout 120 db
        check_pg17
        compose exec -T db sh -eu -c 'export PGPASSWORD="$SUANJIE_DB_PASSWORD"; exec pg_restore -w -h 127.0.0.1 -U suanjie -d suanjie --single-transaction --exit-on-error --no-owner --no-acl' < "$backup/database.dump"
        compose run --no-deps --rm -T --entrypoint tar app -C /data --no-same-owner --no-same-permissions -xf - < "$backup/app-data.tar"
        echo 'Restore loaded into new volumes; app was not started. Verify before switching traffic.'
        ;;
esac
