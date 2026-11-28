#!/usr/bin/env bash
set -Eeuo pipefail
set +x
umask 077
export LC_ALL=C
unset DOCKER_CONTEXT DOCKER_HOST DOCKER_TLS_VERIFY DOCKER_CERT_PATH
unset PRIVATE_PROJECT PRIVATE_POSTGRES_IMAGE PRIVATE_PORT PRIVATE_DB_ADMIN_PASSWORD PRIVATE_DB_PASSWORD PRIVATE_SESSION_SECRET PRIVATE_CRYPTO_SECRET
unset COMPOSE_PROFILES COMPOSE_REMOVE_ORPHANS COMPOSE_ENV_FILES
export COMPOSE_DISABLE_ENV_FILE=1

readonly revision=${ACCEPTANCE_REVISION:?Missing reviewed commit SHA}
readonly expected_revision=6132c5d8cb5d95e28a6444af4def7da780c2776f
readonly here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
readonly repo=$(git -C "$here" rev-parse --show-toplevel)
readonly temp="$RUNNER_TEMP/deploy-01c"
readonly report="$temp/report.txt"
readonly raw="$temp/raw"

mkdir -m 700 -p -- "$raw"
touch "$report"
exec > >(tee -a "$report") 2>&1

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
record() { printf '%s\n' "$*"; }

[[ ${RUNNER_ENVIRONMENT:-} == github-hosted ]] || fail 'Runner is not GitHub-hosted.'
[[ $(uname -s):$(uname -m) == Linux:x86_64 ]] || fail 'Expected Linux amd64.'
[[ $revision =~ ^[0-9a-f]{40}$ ]] || fail 'Invalid acceptance revision.'
[[ $(git -C "$repo" rev-parse HEAD) == "$revision" ]] || fail 'Checked-out revision differs from the event SHA.'
git -C "$repo" cat-file -e "$expected_revision^{commit}" || fail 'Approved source baseline is missing.'

record "RUNNER_ENVIRONMENT=$RUNNER_ENVIRONMENT"
record "RUNNER_OS=${RUNNER_OS:-unknown}"
record "RUNNER_ARCH=${RUNNER_ARCH:-unknown}"
record "IMAGE_OS_VERSION=${ImageOS:-unknown}"
record "IMAGE_VERSION=${ImageVersion:-unknown}"
record "UBUNTU=$( . /etc/os-release; printf '%s %s' "$NAME" "$VERSION_ID")"
record "KERNEL=$(uname -srvo)"
record "CPU=$(nproc) vCPU"
record "MEMORY=$(awk '/MemTotal/ {printf "%.0f MiB", $2/1024}' /proc/meminfo)"
record "DISK=$(df -h "$RUNNER_TEMP" | awk 'NR==2 {printf "size=%s available=%s", $2, $4}')"
record "CHECKOUT_SHA=$revision"

docker version --format 'DOCKER_CLIENT={{.Client.Version}} DOCKER_SERVER={{.Server.Version}}' | tee "$raw/docker-version.txt"
docker compose version --short | sed 's/^/COMPOSE_VERSION=/'
docker buildx version | sed 's/^/BUILDX_VERSION=/'
containerd --version | sed 's/^/CONTAINERD_VERSION=/'
docker info --format 'DOCKER_ROOT={{.DockerRootDir}} STORAGE={{.Driver}} IPV4={{.IPv4Forwarding}} SECURITY={{json .SecurityOptions}}'

for package in docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker.io docker-compose moby-engine moby-cli; do
  version=$(dpkg-query -W -f='${Version}' "$package" 2>/dev/null || true)
  [[ -n $version ]] && record "PREINSTALLED_PACKAGE=$package VERSION=$version" || record "PREINSTALLED_PACKAGE=$package NOT_INSTALLED"
done

sudo iptables-save -c > "$raw/iptables-v4.before"
sudo ip6tables-save -c > "$raw/iptables-v6.before"
sudo nft list ruleset > "$raw/nft.before"
sha256sum "$raw/iptables-v4.before" "$raw/iptables-v6.before" "$raw/nft.before" | sed 's#  .*#  (baseline rule snapshot)#'

readonly docker_key="$temp/docker.asc"
readonly docker_keyring="$temp/docker.gpg"
readonly docker_source=/etc/apt/sources.list.d/deploy-01c-docker.sources
readonly docker_keyring_system=/etc/apt/keyrings/deploy-01c-docker.gpg
created_apt_source=0
created_apt_keyring=0
created_deploy_user=0
cleanup() {
  local rc=$?
  set +e
  if [[ -n ${mock_container:-} ]]; then docker rm -f "$mock_container" >/dev/null 2>&1; fi
  if [[ -n ${mock_network:-} ]]; then docker network rm "$mock_network" >/dev/null 2>&1; fi
  if [[ -n ${ipv6_network:-} ]]; then docker network rm "$ipv6_network" >/dev/null 2>&1; fi
  if (( created_deploy_user )); then sudo userdel deploy-acceptance >/dev/null 2>&1; fi
  if (( created_apt_source )); then sudo rm -f "$docker_source"; fi
  if (( created_apt_keyring )); then sudo rm -f "$docker_keyring_system"; fi
  if [[ -f $raw/iptables-v4.before && -f $raw/iptables-v4.postinstall ]]; then
    sudo iptables-save -c > "$raw/iptables-v4.after"
    sudo ip6tables-save -c > "$raw/iptables-v6.after"
    sudo nft list ruleset > "$raw/nft.after"
    for family in iptables-v4 iptables-v6 nft; do
      before="$raw/$family.postinstall" after="$raw/$family.after"
      if [[ $family != nft ]]; then
        sed -E 's/\[[0-9]+:[0-9]+\]/[0:0]/g' "$before" > "$raw/$family.before.normalized"
        sed -E 's/\[[0-9]+:[0-9]+\]/[0:0]/g' "$after" > "$raw/$family.after.normalized"
        before="$raw/$family.before.normalized" after="$raw/$family.after.normalized"
      fi
      if diff -u "$before" "$after" > "$raw/$family.diff"; then
        record "FIREWALL_${family}_RESTORED=PASS"
      else
        record "FIREWALL_${family}_RESTORED=DIFF"
        cat "$raw/$family.diff"
        rc=1
      fi
    done
  elif [[ -f $raw/iptables-v4.before ]]; then
    record 'POSTINSTALL_FIREWALL_BASELINE=NOT_CAPTURED'
  fi
  if (( rc == 0 )); then record 'CLEANUP=PASS'; else record "CLEANUP=FAIL EXIT=$rc"; fi
  sha256sum "$report" > "$temp/report.sha256"
  exit "$rc"
}
trap cleanup EXIT

sudo install -d -m 0755 /etc/apt/keyrings
[[ ! -e $docker_source && ! -e $docker_keyring_system ]] || fail 'Temporary Docker APT paths already exist.'
curl --fail --silent --show-error --location https://download.docker.com/linux/ubuntu/gpg -o "$docker_key"
fingerprint=$(gpg --batch --show-keys --with-colons "$docker_key" | awk -F: '$1 == "fpr" {print toupper($10); exit}')
record "DOCKER_REPOSITORY_KEY_FINGERPRINT=$fingerprint"
[[ $fingerprint == 9DC858229FC7DD38854AE2D88D81803C0EBFCD88 ]] || fail 'Docker repository signing-key fingerprint changed; stop for owner review.'
gpg --batch --yes --dearmor --output "$docker_keyring" "$docker_key"
created_apt_keyring=1
sudo install -m 0644 "$docker_keyring" "$docker_keyring_system"
created_apt_source=1
printf 'Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: noble\nComponents: stable\nArchitectures: amd64\nSigned-By: %s\n' "$docker_keyring_system" | sudo tee "$docker_source" >/dev/null
sudo apt-get update

packages=(docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin)
versions=()
engine_version=
compose_version=
for package in "${packages[@]}"; do
  candidate=$(apt-cache policy "$package" | awk '/Candidate:/ {print $2; exit}')
  [[ -n $candidate && $candidate != '(none)' ]] || fail "No signed-repository candidate for $package."
  origin=$(apt-cache policy "$package" | awk '/download.docker.com/ {print; exit}')
  [[ -n $origin ]] || fail "Candidate source for $package is not Docker's official APT repository."
  versions+=("$package=$candidate")
  record "DOCKER_APT_CANDIDATE=$package VERSION=$candidate SOURCE=$origin"
  if [[ $package == docker-ce ]]; then engine_version=${candidate#*:}; fi
  if [[ $package == docker-compose-plugin ]]; then compose_version=${candidate#*:}; fi
done
engine_major=${engine_version%%.*}
[[ $engine_major =~ ^[0-9]+$ ]] && (( engine_major >= 28 )) || fail 'Official Docker Engine candidate is below version 28.'
compose_triplet=${compose_version%%[-+~]*}
[[ $compose_triplet =~ ^([0-9]+)\.([0-9]+)\.([0-9]+) ]] || fail 'Could not parse Docker Compose candidate version.'
(( BASH_REMATCH[1] > 2 || (BASH_REMATCH[1] == 2 && (BASH_REMATCH[2] > 24 || (BASH_REMATCH[2] == 24 && BASH_REMATCH[3] >= 4))) )) || fail 'Official Compose candidate is below 2.24.4.'
sudo apt-get -s install "${versions[@]}" > "$raw/apt-install-simulation.txt"
if grep -qE '^Remv ' "$raw/apt-install-simulation.txt"; then
  fail 'The exact Docker package plan removes existing packages; no installation was attempted.'
fi
record 'APT_EXACT_VERSION_INSTALL_SIMULATION=PASS'
sudo apt-get install -y "${versions[@]}"
systemctl is-active --quiet docker || fail 'Docker daemon is not active after the exact-version install.'
docker version --format 'DOCKER_CLIENT={{.Client.Version}} DOCKER_SERVER={{.Server.Version}}' | tee -a "$report"
docker compose version --short | sed 's/^/COMPOSE_VERSION_AFTER_INSTALL=/'
docker buildx version | sed 's/^/BUILDX_VERSION_AFTER_INSTALL=/'
containerd --version | sed 's/^/CONTAINERD_VERSION_AFTER_INSTALL=/'
sudo iptables-save -c > "$raw/iptables-v4.postinstall"
sudo ip6tables-save -c > "$raw/iptables-v6.postinstall"
sudo nft list ruleset > "$raw/nft.postinstall"
for family in iptables-v4 iptables-v6 nft; do
  if diff -q "$raw/$family.before" "$raw/$family.postinstall" >/dev/null; then
    record "DOCKER_INSTALL_${family}_RULES=UNCHANGED"
  else
    record "DOCKER_INSTALL_${family}_RULES=CHANGED"
  fi
done
record 'DOCKER_EXACT_PACKAGE_INSTALL=PASS (disposable hosted runner; runner destruction is rollback boundary)'

readonly pg_tag=docker.io/library/postgres:17.12-bookworm
inspect=$(docker buildx imagetools inspect "$pg_tag")
printf '%s\n' "$inspect" > "$raw/postgres-manifest-inspect.txt"
pg_digest=$(printf '%s\n' "$inspect" | awk '/^Digest:/ {print $2; exit}')
[[ $pg_digest =~ ^sha256:[0-9a-f]{64}$ ]] || fail 'Could not resolve the PostgreSQL 17 manifest digest.'
printf '%s\n' "$inspect" | grep -q 'linux/amd64' || fail 'Pinned PostgreSQL manifest lacks linux/amd64.'
pg_image="$pg_tag@$pg_digest"
record "POSTGRES_IMAGE=$pg_image"

render_env="$raw/render.env"
umask 077
printf 'PRIVATE_PROJECT=suanjie-render-check\nPRIVATE_POSTGRES_IMAGE=%s\nPRIVATE_PORT=39128\nPRIVATE_DB_ADMIN_PASSWORD=synthetic-admin\nPRIVATE_DB_PASSWORD=synthetic-app\nPRIVATE_SESSION_SECRET=synthetic-session\nPRIVATE_CRYPTO_SECRET=synthetic-crypto\n' "$pg_image" > "$render_env"
docker compose --env-file "$render_env" -p suanjie-render-check -f "$here/compose.yml" config --format json > "$raw/private-compose.json"
jq -e '
  .networks.database.internal == true and
  (.services.db.ports // []) == [] and
  .services.app.ports[0].host_ip == "127.0.0.1" and
  (.services.app.ports[0].published | tostring) == "39128" and
  .services.app.environment.REDIS_CONN_STRING == "" and
  .services.app.environment.BATCH_UPDATE_ENABLED == "false" and
  .services.app.logging.driver == "json-file" and
  .services.app.logging.options["max-size"] == "10m" and
  .services.app.logging.options["max-file"] == "3" and
  (.volumes | has("pg-data") and has("app-data"))
' "$raw/private-compose.json" >/dev/null || fail 'Rendered private Compose config violates network, loopback, or log-retention constraints.'
record 'PRIVATE_COMPOSE_LOOPBACK_DB_ISOLATION_AND_LOG_LIMITS=PASS'

for asset in init-db.sql smoke.mjs; do
  [[ -r $here/$asset && ! -L $here/$asset ]] || fail "Required reviewed asset is not a readable regular file: $asset"
done

bash "$here/manage.sh" build

mock_network="deploy-01c-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
mock_container="$mock_network-mock"
mock_port=39127
ipv6_network="deploy-01c-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT-ipv6"
docker network create "$mock_network" >/dev/null
docker run -d --name "$mock_container" --network "$mock_network" \
  -p "127.0.0.1:$mock_port:8080" \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$here/smoke.mjs:/fixture/smoke.mjs:ro" \
  oven/bun:1.4.4@sha256:1d22548f177f4ff38e0c15baa3c2c46c6c2060ba2d1bc573bb4720b407548f1d \
  bun /fixture/smoke.mjs mock >/dev/null
for attempt in {1..30}; do
  if curl --fail --silent --show-error "http://127.0.0.1:$mock_port/health" >/dev/null; then break; fi
  (( attempt < 30 )) || fail 'Loopback-published mock did not become healthy.'
  sleep 1
done
docker inspect --format '{{json .HostConfig.PortBindings}}' "$mock_container" | jq -e '.["8080/tcp"][0].HostIp == "127.0.0.1"' >/dev/null || fail 'Mock port is not bound exclusively to host loopback.'
record 'LOOPBACK_PORT_PUBLISH=PASS'
docker rm -f "$mock_container" >/dev/null
mock_container=
docker network rm "$mock_network" >/dev/null
mock_network=
docker network create --ipv6 --subnet fd42:1c::/64 "$ipv6_network" >/dev/null
docker network inspect "$ipv6_network" | jq -e '.[0].EnableIPv6 == true and .[0].IPAM.Config[0].Subnet == "fd42:1c::/64"' >/dev/null || fail 'Docker IPv6 bridge network did not retain its requested configuration.'
docker network rm "$ipv6_network" >/dev/null
ipv6_network=
record 'DOCKER_IPV4_LOOPBACK_AND_IPV6_NETWORK_CREATE_DELETE=PASS'

if id deploy-acceptance >/dev/null 2>&1; then fail 'Unexpected pre-existing deploy-acceptance account.'; fi
sudo useradd --system --no-create-home --shell /usr/sbin/nologin deploy-acceptance
created_deploy_user=1
if sudo -u deploy-acceptance docker --host unix:///var/run/docker.sock info >/dev/null 2>&1; then
  fail 'Unprivileged deploy-acceptance user can access Docker socket.'
fi
record 'UNPRIVILEGED_DOCKER_SOCKET_DENIED=PASS'

state="$RUNNER_TEMP/deploy-01c/smoke-state"
bash "$here/manage.sh" smoke "$state" "$pg_image"
[[ -f $state/.env && ! -L $state/.env ]] || fail 'Expected only the generated smoke state file for cleanup.'
rm -f -- "$state/.env"
rmdir -- "$state"
[[ ! -e $state ]] || fail 'Generated smoke state was not removed.'
record 'COMPOSE_POSTGRES_INTERNAL_NO_HOST_PORT_AND_API_MOCK_SMOKE=PASS'

printf 'ACCEPTANCE=PASS\nCHECKOUT_SHA=%s\n' "$revision" >> "$report"
