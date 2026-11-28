#!/usr/bin/env bash
set -Eeuo pipefail
set +x
umask 077
export LC_ALL=C COMPOSE_DISABLE_ENV_FILE=1
unset DOCKER_CONTEXT DOCKER_HOST DOCKER_TLS_VERIFY DOCKER_CERT_PATH

readonly here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
readonly manager="$here/manage.sh"
readonly pg_image=${1:?Missing pinned PostgreSQL image}
readonly root=${2:?Missing lifecycle temp root}
readonly original="$root/original"
readonly restored="$root/restored"
readonly app_port=39129
readonly restore_port=39130
readonly expected_revision=6132c5d8cb5d95e28a6444af4def7da780c2776f
readonly bun_image=oven/bun:1.4.4@sha256:1d22548f177f4ff38e0c15baa3c2c46c6c2060ba2d1bc573bb4720b407548f1d

record() { printf '%s\n' "$*"; }
fail() { record "FAIL: $*"; exit 1; }
docker_local() { docker --host unix:///var/run/docker.sock "$@"; }

[[ $pg_image =~ ^docker\.io/library/postgres:17\.[0-9]+-bookworm@sha256:[0-9a-f]{64}$ ]] || fail 'PostgreSQL image must be pinned to an official 17.x Bookworm digest.'
[[ $root == "${RUNNER_TEMP:?}"/deploy-01c/lifecycle && ! -e $root && ! -L $root ]] || fail 'Lifecycle temp path is not fresh and runner-scoped.'
mkdir -m 700 -- "$root"
cleanup_rc=0

cleanup_state() {
  local state=$1 envfile project containers volumes networks
  [[ -e $state ]] || return 0
  [[ $state == "$root/"* && -d $state && ! -L $state ]] || { record 'LIFECYCLE_CLEANUP=REFUSED_PATH'; return 1; }
  envfile="$state/.env"
  [[ -f $envfile && ! -L $envfile ]] || { record 'LIFECYCLE_CLEANUP=FAILED_ENV'; return 1; }
  project=$(sed -n 's/^PRIVATE_PROJECT=//p' "$envfile")
  [[ $project =~ ^suanjie-private-[0-9a-f]{16}$ ]] || { record 'LIFECYCLE_CLEANUP=FAILED_PROJECT'; return 1; }
  docker_local compose --env-file "$envfile" -p "$project" -f "$here/compose.yml" down --volumes --remove-orphans || return 1
  containers=$(docker_local ps -aq --filter "label=com.docker.compose.project=$project")
  volumes=$(docker_local volume ls -q --filter "label=com.docker.compose.project=$project")
  networks=$(docker_local network ls -q --filter "label=com.docker.compose.project=$project")
  [[ -z $containers && -z $volumes && -z $networks ]] || return 1
  rm -rf -- "$state"
  [[ ! -e $state ]] || return 1
  record "LIFECYCLE_CLEANUP=$project=PASS"
}

finish() {
  local rc=$?
  trap - EXIT
  set +e
  cleanup_state "$original" || cleanup_rc=1
  cleanup_state "$restored" || cleanup_rc=1
  rm -f -- "$root/root-credentials.json" || cleanup_rc=1
  [[ ! -e $root/root-credentials.json ]] || cleanup_rc=1
  rmdir -- "$root" 2>/dev/null || cleanup_rc=1
  if (( cleanup_rc == 0 )); then record 'LIFECYCLE_RESOURCE_CLEANUP=PASS'; else record 'LIFECYCLE_RESOURCE_CLEANUP=FAIL'; fi
  if (( rc == 0 && cleanup_rc == 0 )); then record 'PERSISTENCE_ACCEPTANCE=PASS'; else record "PERSISTENCE_ACCEPTANCE=FAIL EXIT=$rc CLEANUP_EXIT=$cleanup_rc"; (( rc != 0 )) || rc=1; fi
  exit "$rc"
}
trap finish EXIT

state_project() { sed -n 's/^PRIVATE_PROJECT=//p' "$1/.env"; }
compose_state() {
  local state=$1 project
  project=$(state_project "$state")
  [[ $project =~ ^suanjie-private-[0-9a-f]{16}$ ]] || fail 'Unexpected lifecycle project id.'
  docker_local compose --env-file "$state/.env" -p "$project" -f "$here/compose.yml" "$@"
}

run_root_acceptance() {
  local state=$1 mode=$2 project output
  project=$(state_project "$state")
  output="$root/root-credentials.json"
  if [[ $mode == initialize ]]; then
    docker_local run --rm --network "${project}_edge" --read-only --cap-drop ALL --security-opt no-new-privileges \
      -v "$here/smoke.mjs:/fixture/smoke.mjs:ro" "$bun_image" bun /fixture/smoke.mjs initialize-root > "$output"
    chmod 600 "$output"
  else
    docker_local run --rm --network "${project}_edge" --read-only --cap-drop ALL --security-opt no-new-privileges \
      -v "$here/smoke.mjs:/fixture/smoke.mjs:ro" -v "$output:/credentials/root.json:ro" \
      "$bun_image" bun /fixture/smoke.mjs login-existing /credentials/root.json >/dev/null
  fi
}

bash "$manager" init "$original" "$pg_image" "$app_port"
bash "$manager" start "$original"
for attempt in {1..60}; do
  if curl --fail --silent --show-error "http://127.0.0.1:$app_port/api/status" > "$root/status.json"; then break; fi
  (( attempt < 60 )) || fail 'Original app did not become healthy.'
  sleep 2
done
jq -e '.success == true and .data.register_enabled == false' "$root/status.json" >/dev/null || fail 'Original app registration is not closed.'
run_root_acceptance "$original" initialize
record 'SYNTHETIC_ROOT_INITIALIZATION_AND_LOGIN=PASS'

marker=$(openssl rand -hex 16)
marker_sql="CREATE TABLE IF NOT EXISTS deploy_acceptance_marker (value text PRIMARY KEY); INSERT INTO deploy_acceptance_marker(value) VALUES ('$marker') ON CONFLICT DO NOTHING;"
compose_state "$original" exec -T db sh -eu -c 'export PGPASSWORD="$SUANJIE_DB_PASSWORD"; exec psql -X -w -h 127.0.0.1 -U suanjie -d suanjie -c "$1"' sh "$marker_sql" >/dev/null
original_project=$(state_project "$original")
app_volume="${original_project}_app-data"
db_volume="${original_project}_pg-data"
docker_local run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$app_volume:/data" --entrypoint /bin/bash "$pg_image" -eu -c 'printf "%s" "$1" > /data/.deploy-acceptance-marker' bash "$marker"
original_volumes=$(docker_local volume inspect --format '{{.Name}} {{.Mountpoint}}' "$app_volume" "$db_volume" | sort)

bash "$manager" stop-app "$original"
compose_state "$original" rm -f app >/dev/null
bash "$manager" start "$original"
for attempt in {1..60}; do
  if curl --fail --silent --show-error "http://127.0.0.1:$app_port/api/status" >/dev/null; then break; fi
  (( attempt < 60 )) || fail 'Recreated app did not become healthy.'
  sleep 2
done
persisted_marker=$(compose_state "$original" exec -T db sh -eu -c 'export PGPASSWORD="$SUANJIE_DB_PASSWORD"; exec psql -X -w -h 127.0.0.1 -U suanjie -d suanjie -Atqc "SELECT value FROM deploy_acceptance_marker"')
[[ $persisted_marker == "$marker" ]] || fail 'Database marker did not survive app recreation.'
file_marker=$(docker_local run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges -v "$app_volume:/data:ro" --entrypoint /bin/bash "$pg_image" -eu -c 'cat /data/.deploy-acceptance-marker')
[[ $file_marker == "$marker" ]] || fail 'App-data marker did not survive app recreation.'
[[ $(docker_local volume inspect --format '{{.Name}} {{.Mountpoint}}' "$app_volume" "$db_volume" | sort) == "$original_volumes" ]] || fail 'Original volume identities changed.'
record 'PERSISTENT_DATA_AFTER_APP_CONTAINER_RECREATION=PASS'

bash "$manager" stop-app "$original"
compose_state "$original" stop db >/dev/null
compose_state "$original" rm -f db >/dev/null
compose_state "$original" up -d --no-deps --wait --wait-timeout 120 db
bash "$manager" start "$original"
recreated_db_marker=$(compose_state "$original" exec -T db sh -eu -c 'export PGPASSWORD="$SUANJIE_DB_PASSWORD"; exec psql -X -w -h 127.0.0.1 -U suanjie -d suanjie -Atqc "SELECT value FROM deploy_acceptance_marker"')
[[ $recreated_db_marker == "$marker" ]] || fail 'Database marker did not survive PostgreSQL container recreation.'
record 'POSTGRES_DATA_AFTER_CONTAINER_RECREATION=PASS'

bash "$manager" stop-app "$original"
backup_output=$(bash "$manager" backup "$original")
backup_path=$(sed -n 's/^Backup complete: \(.*\) (contains secrets; encrypt before transfer)$/\1/p' <<< "$backup_output")
[[ -n $backup_path && -d $backup_path && ! -L $backup_path ]] || fail 'Backup directory was not created.'
(cd "$backup_path" && sha256sum --check SHA256SUMS) >/dev/null || fail 'Backup checksums do not verify.'
bash "$manager" restart "$original"
original_volume_snapshot=$(docker_local volume inspect --format '{{.Name}} {{.Mountpoint}}' "$app_volume" "$db_volume" | sort)
bash "$manager" restore "$restored" "$backup_path" "$restore_port"
restored_project=$(state_project "$restored")
[[ $restored_project != "$original_project" ]] || fail 'Restore reused the original project.'
restored_marker=$(compose_state "$restored" exec -T db sh -eu -c 'export PGPASSWORD="$SUANJIE_DB_PASSWORD"; exec psql -X -w -h 127.0.0.1 -U suanjie -d suanjie -Atqc "SELECT value FROM deploy_acceptance_marker"')
[[ $restored_marker == "$marker" ]] || fail 'Database marker did not survive backup and restore.'
restored_app_volume="${restored_project}_app-data"
restored_file_marker=$(docker_local run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges -v "$restored_app_volume:/data:ro" --entrypoint /bin/bash "$pg_image" -eu -c 'cat /data/.deploy-acceptance-marker')
[[ $restored_file_marker == "$marker" ]] || fail 'App-data marker did not survive backup and restore.'
bash "$manager" start "$restored"
for attempt in {1..60}; do
  if curl --fail --silent --show-error "http://127.0.0.1:$restore_port/api/status" > "$root/restored-status.json"; then break; fi
  (( attempt < 60 )) || fail 'Restored app did not become healthy.'
  sleep 2
done
jq -e '.success == true and .data.register_enabled == false' "$root/restored-status.json" >/dev/null || fail 'Restored app registration is not closed.'
run_root_acceptance "$restored" login
record 'RESTORED_ROOT_LOGIN_AND_PRIVATE_ACCESS_FLAGS=PASS'
[[ $(docker_local volume inspect --format '{{.Name}} {{.Mountpoint}}' "$app_volume" "$db_volume" | sort) == "$original_volume_snapshot" ]] || fail 'Restore modified original volumes.'
record 'MANAGE_BACKUP_RESTORE_NEW_PROJECT_NEW_VOLUMES_AND_SYNTHETIC_DATA=PASS'

bash "$manager" stop-app "$restored"
docker_local run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$restored_app_volume:/data" --entrypoint /bin/bash "$pg_image" -eu -c '
    mkdir -p /data/logs
    printf expired > /data/logs/expired.log
    touch -d "31 days ago" /data/logs/expired.log
    printf recent > /data/logs/recent.log
    truncate -s 220200960 /data/logs/oversized.log
    touch -d "2 days ago" /data/logs/oversized.log
  '
bash "$manager" prune-logs "$restored"
docker_local run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$restored_app_volume:/data:ro" --entrypoint /bin/bash "$pg_image" -eu -c '
    [[ ! -e /data/logs/expired.log && -f /data/logs/recent.log ]]
    bytes=$(du -sb /data/logs | cut -f1)
    (( bytes <= 209715200 ))
  '
record 'PERSISTENT_LOG_MAINTENANCE_30D_AND_200MIB=PASS (manual; not a continuous quota)'
