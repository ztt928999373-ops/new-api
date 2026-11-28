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
exec > "$report" 2>&1

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
record() { printf '%s\n' "$*"; }
docker_local() { docker --host unix:///var/run/docker.sock "$@"; }

snapshot_sysctls() {
  local destination=$1 key
  : > "$destination"
  for key in net.ipv4.ip_forward net.ipv6.conf.all.forwarding net.ipv6.conf.default.forwarding net.ipv6.conf.all.disable_ipv6 net.bridge.bridge-nf-call-iptables net.bridge.bridge-nf-call-ip6tables; do
    value=$(sysctl -n "$key" 2>/dev/null || printf 'UNAVAILABLE')
    printf '%s=%s\n' "$key" "$value" >> "$destination"
  done
}

snapshot_services() {
  local destination=$1 unit state
  : > "$destination"
  for unit in docker containerd ufw ssh aliyun cloudmonitor; do
    state=$(systemctl show --property=ActiveState --value "$unit" 2>/dev/null || printf 'NOT_INSTALLED')
    printf '%s=%s\n' "$unit" "$state" >> "$destination"
  done
}

snapshot_listeners() { ss -H -lntu | sort > "$1"; }

normalize_firewall() {
  local family=$1 source=$2 destination=$3
  if [[ $family == nft ]]; then
    sed -E 's/counter packets [0-9]+ bytes [0-9]+/counter packets COUNTER bytes COUNTER/g; s/ handle [0-9]+//g' "$source" > "$destination"
  else
    sed -E 's/\[[0-9]+:[0-9]+\]/[COUNTERS]/g' "$source" > "$destination"
  fi
}

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

docker_local version --format 'DOCKER_CLIENT={{.Client.Version}} DOCKER_SERVER={{.Server.Version}}' > "$raw/docker-version.txt"
cat "$raw/docker-version.txt"
docker_local compose version --short | sed 's/^/COMPOSE_VERSION=/'
docker_local buildx version | sed 's/^/BUILDX_VERSION=/'
containerd --version | sed 's/^/CONTAINERD_VERSION=/'
docker_local info --format 'DOCKER_ROOT={{.DockerRootDir}} STORAGE={{.Driver}} IPV4={{.IPv4Forwarding}} SECURITY={{json .SecurityOptions}}'

for package in docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker.io docker-compose moby-engine moby-cli; do
  version=$(dpkg-query -W -f='${Version}' "$package" 2>/dev/null || true)
  [[ -n $version ]] && record "PREINSTALLED_PACKAGE=$package VERSION=$version" || record "PREINSTALLED_PACKAGE=$package NOT_INSTALLED"
done

sudo iptables-save -c > "$raw/iptables-v4.before"
sudo ip6tables-save -c > "$raw/iptables-v6.before"
sudo nft list ruleset > "$raw/nft.before"
dpkg-query -W -f='${binary:Package}\t${Version}\n' | sort > "$raw/packages.before"
snapshot_sysctls "$raw/sysctls.before"
snapshot_services "$raw/services.before"
snapshot_listeners "$raw/listeners.before"
sha256sum "$raw/iptables-v4.before" "$raw/iptables-v6.before" "$raw/nft.before" | sed 's#  .*#  (baseline rule snapshot)#'

readonly docker_key="$temp/docker.asc"
readonly docker_keyring="$temp/docker.gpg"
readonly docker_source=/etc/apt/sources.list.d/deploy-01c-docker.sources
readonly docker_keyring_system=/etc/apt/keyrings/deploy-01c-docker.gpg
created_apt_source=0
created_apt_keyring=0
created_deploy_user=0
tests_complete=0
cleanup_rc=0
mock_container=
mock_network=
ipv6_container=
ipv6_network=
cleanup() {
  local rc=$?
  set +e
  if [[ -n ${mock_container:-} ]]; then
    docker_local rm -f "$mock_container" >/dev/null || cleanup_rc=1
    docker_local inspect "$mock_container" >/dev/null 2>&1 && cleanup_rc=1
  fi
  if [[ -n ${ipv6_container:-} ]]; then
    docker_local rm -f "$ipv6_container" >/dev/null || cleanup_rc=1
    docker_local inspect "$ipv6_container" >/dev/null 2>&1 && cleanup_rc=1
  fi
  if [[ -n ${mock_network:-} ]]; then
    docker_local network rm "$mock_network" >/dev/null || cleanup_rc=1
    docker_local network inspect "$mock_network" >/dev/null 2>&1 && cleanup_rc=1
  fi
  if [[ -n ${ipv6_network:-} ]]; then
    docker_local network rm "$ipv6_network" >/dev/null || cleanup_rc=1
    docker_local network inspect "$ipv6_network" >/dev/null 2>&1 && cleanup_rc=1
  fi
  if (( created_deploy_user )); then
    sudo userdel deploy-acceptance >/dev/null || cleanup_rc=1
    id deploy-acceptance >/dev/null 2>&1 && cleanup_rc=1
  fi
  if (( created_apt_source )); then sudo rm -f "$docker_source" || cleanup_rc=1; fi
  if (( created_apt_keyring )); then sudo rm -f "$docker_keyring_system" || cleanup_rc=1; fi
  if [[ -f $raw/iptables-v4.before && -f $raw/iptables-v4.postinstall ]]; then
    sudo iptables-save -c > "$raw/iptables-v4.after" || cleanup_rc=1
    sudo ip6tables-save -c > "$raw/iptables-v6.after" || cleanup_rc=1
    sudo nft list ruleset > "$raw/nft.after" || cleanup_rc=1
    snapshot_sysctls "$raw/sysctls.after" || cleanup_rc=1
    snapshot_services "$raw/services.after" || cleanup_rc=1
    snapshot_listeners "$raw/listeners.after" || cleanup_rc=1
    for family in iptables-v4 iptables-v6 nft; do
      before="$raw/$family.postinstall" after="$raw/$family.after"
      normalize_firewall "$family" "$before" "$raw/$family.before.normalized"
      normalize_firewall "$family" "$after" "$raw/$family.after.normalized"
      before="$raw/$family.before.normalized" after="$raw/$family.after.normalized"
      if diff -u "$before" "$after" > "$raw/$family.diff"; then
        record "FIREWALL_${family}_AFTER_TEST_CLEANUP=UNCHANGED"
      else
        record "FIREWALL_${family}_AFTER_TEST_CLEANUP=CHANGED"
        cleanup_rc=1
      fi
    done
    for snapshot in sysctls services listeners; do
      if diff -u "$raw/$snapshot.postinstall" "$raw/$snapshot.after" > "$raw/$snapshot.cleanup.diff"; then record "${snapshot^^}_AFTER_TEST_CLEANUP=UNCHANGED"; else record "${snapshot^^}_AFTER_TEST_CLEANUP=CHANGED"; cleanup_rc=1; fi
    done
  elif [[ -f $raw/iptables-v4.before ]]; then
    record 'POSTINSTALL_FIREWALL_BASELINE=NOT_CAPTURED'
  fi
  if (( cleanup_rc == 0 )); then record 'CLEANUP=PASS'; else record 'CLEANUP=FAIL'; fi
  if (( rc == 0 && cleanup_rc == 0 && tests_complete == 1 )); then
    record 'ACCEPTANCE=PASS'
  else
    record "ACCEPTANCE=FAIL EXIT=$rc CLEANUP_EXIT=$cleanup_rc TESTS_COMPLETE=$tests_complete"
    (( rc != 0 )) || rc=1
    (( cleanup_rc == 0 )) || rc=1
    (( tests_complete == 1 )) || rc=1
  fi
  sha256sum "$report" > "$temp/report.sha256" || rc=1
  exit "$rc"
}
trap cleanup EXIT

source_matches=$(sudo grep -RIlE 'download\.docker\.com/linux/ubuntu' /etc/apt/sources.list /etc/apt/sources.list.d 2>/dev/null || true)
if [[ -n $source_matches ]]; then
  while IFS= read -r path; do
    [[ -n $path ]] || continue
    record "DOCKER_APT_EXISTING_SOURCE_PATH=$path"
    sudo grep -E '^[[:space:]]*(URIs:|Signed-By:|deb[[:space:]])' "$path" | sed -E 's#(https?://)[^/[:space:]]+#\1<repository-host>#g' | while IFS= read -r line; do record "DOCKER_APT_EXISTING_SOURCE_POLICY=$line"; done
  done <<< "$source_matches"
  fail 'Existing Docker APT source/Signed-By policy requires owner review; no duplicate source was added.'
fi
record 'DOCKER_APT_EXISTING_SOURCE=NONE'
record 'DOCKER_APT_SIGNED_BY_POLICY=Dedicated keyring; no global key added'
sudo install -d -m 0755 /etc/apt/keyrings
[[ ! -e $docker_source && ! -e $docker_keyring_system ]] || fail 'Temporary Docker APT paths already exist.'
curl --fail --silent --show-error --location https://download.docker.com/linux/ubuntu/gpg -o "$docker_key"
key_metadata=$(gpg --batch --show-keys --with-colons "$docker_key")
primary_key_count=$(printf '%s\n' "$key_metadata" | awk -F: '$1 == "pub" {count++} END {print count+0}')
fingerprint=$(printf '%s\n' "$key_metadata" | awk -F: '$1 == "fpr" {print toupper($10); exit}')
record "DOCKER_REPOSITORY_PRIMARY_KEY_COUNT=$primary_key_count"
record "DOCKER_REPOSITORY_KEY_FINGERPRINT=$fingerprint"
[[ $primary_key_count == 1 && $fingerprint == 9DC858229FC7DD38854AE2D88D81803C0EBFCD88 ]] || fail 'Docker repository signing-key identity changed; stop for owner review.'
gpg --batch --yes --dearmor --output "$docker_keyring" "$docker_key"
created_apt_keyring=1
sudo install -m 0644 "$docker_keyring" "$docker_keyring_system"
created_apt_source=1
printf 'Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: noble\nComponents: stable\nArchitectures: amd64\nSigned-By: %s\n' "$docker_keyring_system" | sudo tee "$docker_source" >/dev/null
sudo apt-get update

packages=(docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin)
versions=()
declare -A candidates=() plan_versions=()
engine_version=
compose_version=
for package in "${packages[@]}"; do
  policy=$(apt-cache policy "$package")
  candidate=$(awk '$1 == "Candidate:" {print $2; exit}' <<< "$policy")
  [[ -n $candidate && $candidate != '(none)' ]] || fail "No signed-repository candidate for $package."
  candidate_origins=$(awk -v candidate="$candidate" '
    {
      line=$0; sub(/^[[:space:]]+/, "", line)
      if (line ~ /^\*\*\*/) { sub(/^\*\*\*[[:space:]]+/, "", line); split(line, fields, /[[:space:]]+/); in_candidate=(fields[1] == candidate); next }
      split(line, fields, /[[:space:]]+/)
      if (fields[1] == candidate && fields[2] ~ /^[0-9]+$/) { in_candidate=1; next }
      if (in_candidate && line ~ /^[0-9]+[[:space:]]+https?:\/\//) print line
    }
  ' <<< "$policy")
  [[ $candidate_origins =~ ^[0-9]+[[:space:]]+https://download\.docker\.com/linux/ubuntu[[:space:]]+noble/stable[[:space:]]+amd64[[:space:]]+Packages$ ]] || fail "Candidate $package=$candidate is not uniquely from Docker's approved Noble stable amd64 index."
  candidates[$package]=$candidate
  versions+=("$package=$candidate")
  record "DOCKER_APT_CANDIDATE=$package VERSION=$candidate ORIGIN=$candidate_origins"
  if [[ $package == docker-ce ]]; then engine_version=${candidate#*:}; fi
  if [[ $package == docker-compose-plugin ]]; then compose_version=${candidate#*:}; fi
done
engine_major=${engine_version%%.*}
[[ $engine_major =~ ^[0-9]+$ ]] && (( engine_major >= 28 )) || fail 'Official Docker Engine candidate is below version 28.'
compose_triplet=${compose_version%%[-+~]*}
[[ $compose_triplet =~ ^([0-9]+)\.([0-9]+)\.([0-9]+) ]] || fail 'Could not parse Docker Compose candidate version.'
(( BASH_REMATCH[1] > 2 || (BASH_REMATCH[1] == 2 && (BASH_REMATCH[2] > 24 || (BASH_REMATCH[2] == 24 && BASH_REMATCH[3] >= 4))) )) || fail 'Official Compose candidate is below 2.24.4.'
sudo apt-get -s install "${versions[@]}" > "$raw/apt-install-simulation.txt"
grep -qE '^Remv ' "$raw/apt-install-simulation.txt" && fail 'Install simulation removes packages; installation was not attempted.'
while IFS= read -r line; do
  [[ $line == Inst\ * ]] || continue
  package=$(awk '{print $2}' <<< "$line")
  package=${package%%:*}
  old_version=$(sed -nE 's/^Inst [^ ]+ \[([^]]+)\].*/\1/p' <<< "$line")
  new_version=$(sed -nE 's/^Inst [^ ]+ (\[[^]]+\] )?\(([^ ]+).*/\2/p' <<< "$line")
  [[ -n $new_version ]] || fail "Could not parse simulated version for $package."
  [[ -v candidates[$package] ]] || fail "Install simulation changes an unapproved package $package; review dependencies before retrying."
  if [[ -n $old_version ]]; then
    dpkg --compare-versions "$new_version" ge "$old_version" || fail "Install simulation downgrades $package."
  fi
  [[ ${candidates[$package]} == "$new_version" ]] || fail "Simulated Docker package version differs from its approved candidate: $package."
  plan_versions[$package]=$new_version
  record "APT_SIMULATED_CHANGE=$package OLD=${old_version:-ABSENT} NEW=$new_version"
done < "$raw/apt-install-simulation.txt"
record 'APT_EXACT_VERSION_INSTALL_SIMULATION=PASS (removals, downgrades, and all non-target changes rejected)'
sudo apt-get install -y "${versions[@]}"
systemctl is-active --quiet docker || fail 'Docker daemon is not active after the exact-version install.'
systemctl is-active --quiet containerd || fail 'containerd is not active after the exact-version install.'
for package in "${packages[@]}"; do
  actual=$(dpkg-query -W -f='${Version}' "$package")
  [[ $actual == "${candidates[$package]}" ]] || fail "Installed version differs from the approved candidate: $package."
  record "DOCKER_APT_INSTALLED=$package VERSION=$actual"
done
docker_local version --format 'DOCKER_CLIENT={{.Client.Version}} DOCKER_SERVER={{.Server.Version}}'
docker_local compose version --short | sed 's/^/COMPOSE_VERSION_AFTER_INSTALL=/'
docker_local buildx version | sed 's/^/BUILDX_VERSION_AFTER_INSTALL=/'
containerd --version | sed 's/^/CONTAINERD_VERSION_AFTER_INSTALL=/'
sudo iptables-save -c > "$raw/iptables-v4.postinstall"
sudo ip6tables-save -c > "$raw/iptables-v6.postinstall"
sudo nft list ruleset > "$raw/nft.postinstall"
dpkg-query -W -f='${binary:Package}\t${Version}\n' | sort > "$raw/packages.postinstall"
snapshot_sysctls "$raw/sysctls.postinstall"
snapshot_services "$raw/services.postinstall"
snapshot_listeners "$raw/listeners.postinstall"
for family in iptables-v4 iptables-v6 nft; do
  normalize_firewall "$family" "$raw/$family.before" "$raw/$family.before.normalized"
  normalize_firewall "$family" "$raw/$family.postinstall" "$raw/$family.postinstall.normalized"
  if diff -q "$raw/$family.before.normalized" "$raw/$family.postinstall.normalized" >/dev/null; then
    record "DOCKER_INSTALL_${family}_RULES=UNCHANGED"
  else
    record "DOCKER_INSTALL_${family}_RULES=CHANGED"
  fi
done
for snapshot in sysctls services listeners; do
  if diff -q "$raw/$snapshot.before" "$raw/$snapshot.postinstall" >/dev/null; then
    record "DOCKER_INSTALL_${snapshot^^}=UNCHANGED"
  else
    record "DOCKER_INSTALL_${snapshot^^}=CHANGED"
  fi
done
changed_package_count=$(diff -U0 "$raw/packages.before" "$raw/packages.postinstall" | awk '/^[+-][^+-]/ {count++} END {print count+0}')
record "DOCKER_INSTALL_PACKAGE_MANIFEST_DIFF_LINES=$changed_package_count"
record 'DOCKER_EXACT_PACKAGE_INSTALL=PASS (disposable hosted runner; runner destruction is rollback boundary)'

docker_local info > "$raw/docker-info.txt"
grep -E '^[[:space:]]*(Docker Root Dir|Storage Driver|IPv4 Forwarding|IPv6|Firewall Backend|Experimental):' "$raw/docker-info.txt" | sed -E 's/^[[:space:]]*//' | while IFS= read -r line; do record "DOCKER_INFO=$line"; done
if sudo test -f /etc/docker/daemon.json; then
  sudo jq '{"iptables": .iptables, "ip6tables": .ip6tables, "allow-direct-routing": .["allow-direct-routing"], "ipv6": .ipv6, "fixed-cidr-v6": .["fixed-cidr-v6"], "firewall-backend": .["firewall-backend"]}' /etc/docker/daemon.json > "$raw/docker-daemon-network-settings.json" || fail 'Docker daemon JSON settings are invalid.'
  jq -e '(.iptables // true) != false and (.ip6tables // true) != false and (.["allow-direct-routing"] // false) == false' "$raw/docker-daemon-network-settings.json" >/dev/null || fail 'Docker daemon disables firewall rules or enables direct routing.'
  record 'DOCKER_DAEMON_NETWORK_SETTINGS=RECORDED_AND_SAFE'
else
  printf '{}\n' > "$raw/docker-daemon-network-settings.json"
  record 'DOCKER_DAEMON_NETWORK_SETTINGS=DEFAULTS_NO_DAEMON_JSON'
fi

readonly pg_tag=docker.io/library/postgres:17.12-bookworm
inspect=$(docker_local buildx imagetools inspect "$pg_tag")
printf '%s\n' "$inspect" > "$raw/postgres-manifest-inspect.txt"
pg_digest=$(printf '%s\n' "$inspect" | awk '/^Digest:/ {print $2; exit}')
[[ $pg_digest =~ ^sha256:[0-9a-f]{64}$ ]] || fail 'Could not resolve the PostgreSQL 17 manifest digest.'
printf '%s\n' "$inspect" | grep -q 'linux/amd64' || fail 'Pinned PostgreSQL manifest lacks linux/amd64.'
pg_image="$pg_tag@$pg_digest"
record "POSTGRES_IMAGE=$pg_image"

render_env="$raw/render.env"
umask 077
printf 'PRIVATE_PROJECT=suanjie-render-check\nPRIVATE_POSTGRES_IMAGE=%s\nPRIVATE_PORT=39128\nPRIVATE_DB_ADMIN_PASSWORD=synthetic-admin\nPRIVATE_DB_PASSWORD=synthetic-app\nPRIVATE_SESSION_SECRET=synthetic-session\nPRIVATE_CRYPTO_SECRET=synthetic-crypto\n' "$pg_image" > "$render_env"
docker_local compose --env-file "$render_env" -p suanjie-render-check -f "$here/compose.yml" config --format json > "$raw/private-compose.json"
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

for asset in init-db.sql smoke.mjs log-retention.sh persistence-acceptance.sh; do
  [[ -r $here/$asset && ! -L $here/$asset ]] || fail "Required reviewed asset is not a readable regular file: $asset"
done

bash "$here/manage.sh" build

mock_network="deploy-01c-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
mock_container="$mock_network-mock"
mock_port=39127
ipv6_network="deploy-01c-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT-ipv6"
docker_local network create "$mock_network" >/dev/null
docker_local run -d --name "$mock_container" --network "$mock_network" \
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
docker_local inspect --format '{{json .HostConfig.PortBindings}}' "$mock_container" | jq -e '.["8080/tcp"][0].HostIp == "127.0.0.1"' >/dev/null || fail 'Mock port is not bound exclusively to host loopback.'
non_loopback_v4=0
while IFS= read -r host_ip; do
  [[ -n $host_ip ]] || continue
  if curl --fail --silent --show-error --connect-timeout 2 --max-time 3 "http://$host_ip:$mock_port/health" >/dev/null 2>&1; then fail 'Loopback service is reachable on a non-loopback host address.'; fi
  non_loopback_v4=$((non_loopback_v4 + 1))
done < <(ip -o -4 addr show scope global | awk '{split($4,address,"/");print address[1]}')
(( non_loopback_v4 > 0 )) || fail 'No non-loopback IPv4 address was available for the exposure test.'
non_loopback_v6=0
while IFS= read -r host_ip; do
  [[ -n $host_ip ]] || continue
  if curl --fail --silent --show-error --connect-timeout 2 --max-time 3 "http://[$host_ip]:$mock_port/health" >/dev/null 2>&1; then fail 'Loopback service is reachable on a non-loopback IPv6 host address.'; fi
  non_loopback_v6=$((non_loopback_v6 + 1))
done < <(ip -o -6 addr show scope global | awk '{split($4,address,"/");print address[1]}')
record "LOOPBACK_PUBLISH_NON_LOOPBACK_DENIED=PASS IPV4_PROBES=$non_loopback_v4 IPV6_PROBES=$non_loopback_v6 (runner-local probes; not an Internet scan)"
docker_local rm -f "$mock_container" >/dev/null
mock_container=
docker_local network rm "$mock_network" >/dev/null
mock_network=
ipv6_network="deploy-01c-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT-ipv6"
ipv6_container="${ipv6_network}-server"
docker_local network create --ipv6 --internal --subnet fd42:1c::/64 "$ipv6_network" >/dev/null
docker_local network inspect "$ipv6_network" | jq -e '.[0] | .EnableIPv6 == true and any(.IPAM.Config[]?; .Subnet == "fd42:1c::/64")' >/dev/null || fail 'Docker IPv6 network lacks the expected subnet.'
docker_local run -d --name "$ipv6_container" --network "$ipv6_network" --ip6 fd42:1c::10 \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  oven/bun:1.4.4@sha256:1d22548f177f4ff38e0c15baa3c2c46c6c2060ba2d1bc573bb4720b407548f1d \
  bun -e 'Bun.serve({hostname:"::",port:8081,fetch:()=>new Response("ipv6-local-ok")})' >/dev/null
ipv6_connected=0
for attempt in {1..30}; do
  if docker_local run --rm --network "$ipv6_network" oven/bun:1.4.4@sha256:1d22548f177f4ff38e0c15baa3c2c46c6c2060ba2d1bc573bb4720b407548f1d \
    bun -e 'const r=await fetch("http://[fd42:1c::10]:8081",{signal:AbortSignal.timeout(1500)});if((await r.text())!=="ipv6-local-ok")process.exit(1)' >/dev/null 2>&1; then ipv6_connected=1; break; fi
  sleep 1
done
(( ipv6_connected == 1 )) || fail 'Containers on the IPv6 test network could not communicate.'
docker_local inspect --format '{{json .HostConfig.PortBindings}}' "$ipv6_container" | jq -e '. == null or length == 0' >/dev/null || fail 'IPv6 test server unexpectedly published a host port.'
if docker_local run --rm --network none oven/bun:1.4.4@sha256:1d22548f177f4ff38e0c15baa3c2c46c6c2060ba2d1bc573bb4720b407548f1d \
  bun -e 'try{await fetch("http://[fd42:1c::10]:8081",{signal:AbortSignal.timeout(1500)});process.exit(1)}catch{process.exit(0)}' >/dev/null 2>&1; then
  record 'DOCKER_IPV6_CONTAINER_COMMUNICATION_AND_NETWORK_ISOLATION=PASS'
else
  fail 'Container outside the isolated IPv6 network reached its synthetic server.'
fi
record 'DOCKER_IPV6_HOST_PUBLICATION=NONE (not an Internet reachability test)'

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

bash "$here/persistence-acceptance.sh" "$pg_image" "$RUNNER_TEMP/deploy-01c/lifecycle"
record 'PERSISTENCE_BACKUP_RESTORE_AND_LOG_RETENTION=PASS'

tests_complete=1
record 'ALL_REQUIRED_TESTS=PASS'
