# DEPLOY-01C GitHub-hosted runner acceptance plan

This workflow is an opt-in, disposable-host test. It is not evidence that Docker was installed on the Alibaba Cloud server and it does not authorize server installation or public operation.

## Trigger and permissions

`.github/workflows/deploy-01c-runner-acceptance.yml` uses only GitHub-hosted `ubuntu-24.04` and listens only to `pull_request.labeled` events targeting `product/v1`; it does not rely on `workflow_dispatch`, which cannot be manually run for a workflow that is not present on the default branch. The job runs only for this same-repository branch, the repository owner as event actor, a non-draft PR, and the `deploy-01c-run-approved` label added by that event. Checkout uses the event's exact PR head SHA. A later commit does not inherit that execution approval. Remove and re-add the label after reviewing the new SHA. Rerunning an old workflow run reuses its original event payload and SHA; it does not approve or test the current branch tip.

The job has `contents: read`, no secrets, no package-write permission, no deployment environment, no self-hosted runner, and no production network target. It checks out the exact PR head with persisted Git credentials disabled. Artifacts contain only a one-day sanitized report; credentials, `.env`, full container logs, and private state are excluded.

## Cost and runner baseline

The repository is public. GitHub documents standard hosted runners as free for public repositories; its current public-repository `ubuntu-24.04` x64 VM specification is 4 vCPU, 16 GB RAM, and 14 GB SSD. The job is capped at 60 minutes and the report artifact is retained for one day. The repository owner's account-level usage and organization-specific policies remain an owner-side check. Runner image and hardware are recorded at runtime because hosted image contents can change.

Opening a Draft PR can still start unrelated repository workflows. In particular, the existing `pull_request_target` AI manager may send its configured Telegram PR notification. The new acceptance job itself remains skipped until the owner explicitly adds the approval label to a ready PR.

## Runtime coverage

The runner script records the hosted-runner environment, image metadata when exposed, OS, kernel, vCPU, memory, disk, checkout SHA, Docker Engine/CLI, Compose, Buildx, containerd, and relevant preinstalled package versions. It snapshots IPv4, IPv6, and nftables state before Docker network testing, exercises loopback publishing and IPv6 bridge creation/deletion, then compares restored rule sets afterward.

It reuses exactly one existing Docker DEB822 source only when URI, Noble suite, stable component, amd64 architecture and Signed-By key fingerprint match the approved policy; unsupported fields, conflicts or invalid source files stop the run. If none exists, it adds a dedicated signed Noble APT source. It resolves exact versions of Engine, CLI, containerd, Buildx, and Compose, verifies each candidate source, simulates the exact install, and installs only if APT proposes no removals, downgrades, or package changes beyond those five explicitly selected packages. Any additional dependency change stops the run for review. It records firewall rules before and after installation. Docker/network test resources are removed and the final firewall rules are compared with the post-install baseline. The runner is disposable, so destruction (not host package downgrade) is the package and firewall rollback boundary. This distinguishes stock-runner package installation from a clean Ubuntu image install; no claim of a clean-system install is made.

The existing `manage.sh build` builds only source commit `6132c5d8cb5d95e28a6444af4def7da780c2776f`, verifies OCI identity and LICENSE/NOTICE/third-party license file hashes, and does not push an image. The existing `manage.sh smoke` reuses the isolated synthetic PostgreSQL/API-key/mock-model workflow, with a resolved PostgreSQL 17.12 Bookworm digest. The lifecycle acceptance initializes and logs in a synthetic Root account, recreates both app and PostgreSQL containers while verifying their volume data, runs `manage.sh backup` and `restore` into a new project, verifies restored Root login and closed registration/top-up flags, confirms original volumes remain untouched, and exercises stopped-app `/data/logs` maintenance. It does not call a real model supplier. A separate pinned Bun container verifies loopback-only host publication; an ephemeral unprivileged account checks Docker socket denial.

The runner is destroyed after completion. That is the rollback boundary for runner-local package and firewall state; this workflow installs exact reviewed candidates only on that one-use hosted VM, never on the Alibaba server. The app smoke's Compose cleanup removes only its uniquely named synthetic project, network, and volumes. Full firewall snapshots remain private to the runner; the artifact reports only whether rules changed and were stable after cleanup.

## Not covered by this workflow

- The GitHub-hosted Ubuntu 24.04 runner may arrive with Docker preinstalled; the workflow records that baseline and tests exact package installation on that disposable runner. It does not verify installation on a clean Ubuntu VM with no Docker packages.
- Actual rollback of an Engine package upgrade on a clean host.
- Production backup encryption, off-host transfer, and disaster recovery remain outside this disposable synthetic check. The workflow verifies only runner-local synthetic volume survival, `manage.sh backup`/`restore` into a new project, and bounded application-file retention.
- Alibaba Cloud kernel, VirtIO, cloud-agent, IPv6, firewall, and resource behavior.
- GHCR publication, production deployment, registration/payment readiness, real funds, or real supplier calls.
- UFW acceptance, which remains DEPLOY-UFW-01; kernel/Retbleed acceptance remains DEPLOY-KERNEL-01.
- The Compose `json-file` limit bounds stdout/stderr only. Application files under `/data/logs` are persistent volume data and are pruned by a manual maintenance command after stopping the app; 30-day/200 MiB cleanup is not a continuous quota and does not provide centralized or encrypted log export.

The result can support a later, separately authorized server Docker installation review only after the workflow passes on the exact reviewed SHA. It cannot substitute for post-install verification on the Alibaba instance.
