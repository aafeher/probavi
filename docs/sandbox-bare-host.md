# Bare-host SSH sandbox provider — design spec

Status: **DRAFT — awaiting maintainer approval. Not normative, no code
until approved** (AGENTS.md §5.1). Tracked by the ROADMAP Phase 4 item.

This document answers the four questions that made bare-host drills a
spec-first task: how isolation, engine/tool version matching, ephemeral
storage, and guaranteed cleanup survive **without a container runtime** on
the target. Where a guarantee cannot survive, this document says so
instead of pretending.

## 1. When to use it — and when not to

The default answer for "run drills on another machine" is the docker
provider over the CLI's native SSH transport (`DOCKER_HOST=ssh://…`,
README "Remote Docker over SSH"): every container guarantee holds
unchanged. **Use the bare-host provider only when the target cannot run a
container runtime at all** — appliance-like DB hosts, restrictive
policies, niche platforms.

The non-negotiable operational premise: **the target host is dedicated to
drills.** It runs no other database, serves no other tenant, and holds
nothing you would mind a restored production copy briefly living next to.
Every residual risk in §6 assumes this premise; without it the provider
must not be used.

## 2. Model

One sandbox = one **transient systemd slice** plus one **per-drill
workspace directory** on the target host, driven exclusively through the
OpenSSH client binary (`ssh` — never a Go SSH library, same no-SDK rule
as docker/kubectl; the operator's `~/.ssh/config`, agent, and
`known_hosts` apply unchanged, and host key verification is never
disabled by the provider).

- **Slice** `probavi-sbx-<suffix>.slice` is the resource and lifetime
  boundary. Every command the sandbox ever runs — provider verbs and the
  adapter-started engine alike — executes as a transient unit inside this
  slice (`systemd-run --slice=…`). Resource caps (`MemoryMax`,
  `CPUQuota`) sit on the slice, so they bound the *sum* of everything in
  the sandbox, exactly like a container's cgroup.
- **Workspace** `<workspace_root>/probavi-sbx-<suffix>/` holds the engine
  data directory, unix sockets, logs, and `scratch/` (the protocol §6.2
  `scratch_dir`). An `owner` marker file records the drill host's
  `HostID()` and pid for the sweep (§5).
- There is no container entrypoint, so bare-host sandboxes are always the
  **idle pattern** the physical-restore flow already uses: `Create` only
  establishes slice + workspace; the adapter starts and owns the engine
  through `exec` verbs. Because those verbs run inside the slice, the
  engine cannot escape the sandbox's lifetime: stopping the slice kills
  the whole process tree, however it was started.

Lifecycle mapping (command shapes, subject to implementation detail):

| Contract call | On the target (via ssh) |
|---|---|
| `Create` | `mkdir -p` workspace + `scratch/`, write `owner` marker; verify `systemd-run` works; arm the deadline backstop (§5). |
| `Exec` | `systemd-run --slice=<slice> --wait --pipe --collect --same-dir=<workspace> env K=V… argv…` — stdin/stdout/stderr stream over the ssh connection; the §4.1 capture caps apply on the drill host as everywhere else. |
| `PutFile` | `ssh <target> sh -c 'cat > "$1" && chmod "$2" "$1"' sh <dest> <mode>` with the local file on stdin — the k8s provider's positional trick; bytes cross only the ssh connection. |
| `Destroy` | `systemctl stop <slice>` (kills every descendant), then `rm -rf` workspace. Idempotent: a missing slice or workspace is success. |
| `SweepOrphans` | List `probavi-sbx-*` slices and workspaces; read `owner` markers; remove those owned by this drill host whose pid is dead. Other hosts' sandboxes are never touched (same host-scoping as docker/k8s). |

## 3. Target host requirements

- Dedicated to drills (§1) — REQUIRED, not advisory.
- systemd as PID 1 with `systemd-run`/`systemctl` available to the drill
  user (v244+ gives everything used here; exact floor fixed at
  implementation time).
- The engine toolchain(s) installed **at versions matching the backups
  under test** — see §4.
- A dedicated OS user for drills, key-based SSH only. Root is not
  required if the operator grants the drill user transient-unit rights
  (system-level via polkit, or `systemd-run --user` with lingering
  enabled — chosen at implementation time; `--user` mode cannot enforce
  `MemoryMax` on some cgroup setups, which the probe must detect and
  report rather than silently drop).

## 4. What containers gave us — and what survives

| Guarantee | Docker/K8s | Bare host |
|---|---|---|
| Engine/tool version match | image tag pins both | **Operator duty.** The drill config's expectations meet whatever is installed; a mismatch surfaces as an honest `restore_failed`, not a silent wrong-version pass — but keeping versions aligned is on the operator. Documented, not solved. |
| Network isolation | `--network none` / NetworkPolicy | Engines are restored listening on unix sockets in the workspace, or loopback with a per-drill port when the engine's tooling demands TCP. Nothing binds beyond loopback by design; host-level firewalling stays the operator's job. |
| Ephemeral storage | volumes die with the container | Workspace `rm -rf` on destroy. Bytes are deleted, not shredded: restored production data touches the target's persistent disks. Mitigations the docs will recommend: workspace on tmpfs, or full-disk encryption on the target. |
| Resource caps | cgroup per container | cgroup per slice — equivalent. |
| Forced destruction | `rm -f` / Job deadline | Slice stop kills the tree; deadline backstop in §5. |
| Clean slate between drills | new container | new workspace + empty slice; anything an adapter wrote outside the workspace is NOT reset — adapters already must not do that (protocol §6.4), and the dedicated-host premise bounds the blast radius. |

## 5. Cleanup guarantees

Three independent layers, mirroring the k8s provider's philosophy:

1. **Normal path:** `Destroy` stops the slice and removes the workspace;
   the core calls it on every drill outcome.
2. **Crashed drill host:** the next drill (from the same host) sweeps:
   `owner` marker names a dead pid → slice stopped, workspace removed.
   Host-scoped exactly like docker/k8s — several drill hosts may share
   one target.
3. **Drill host never comes back:** the target-side backstop, armed at
   `Create`: a transient timer (`systemd-run --on-active=<deadline>
   --timer-property=AccuracySec=1m systemctl stop <slice> …`) stops the
   slice and removes the workspace after the drill's hard deadline — the
   bare-host analog of the k8s `activeDeadlineSeconds`. Production data
   does not outlive a vanished drill host.

## 6. Security posture and residual risk (to be documented verbatim)

- The drill user's commands are visible in the target's process list,
  including `env K=V` prefixes carrying the ephemeral sandbox password —
  acceptable **only** on a dedicated host (§1); the implementation SHOULD
  prefer a 0600 env file in the workspace over argv env where the engine
  allows it.
- The ssh target (user@host) is connection detail: it lives in an
  environment variable (`PROBAVI_SSH_TARGET`), never in drill config —
  sandbox params enter signed evidence records verbatim, and
  evidence-schema §8 forbids connection details there. Same rule as
  credentials and `DOCKER_HOST`.
- Host keys: standard OpenSSH verification against the operator's
  `known_hosts`. The provider never passes `StrictHostKeyChecking=no`.
- Restored production data resides on the target's disks for the drill's
  duration and is deleted, not shredded (§4). tmpfs/FDE recommendations
  become part of the README section.

## 7. Configuration sketch

```yaml
sandbox:
  provider: bare        # name open — see §8
  params:
    workspace_root: /var/lib/probavi-drills   # default TBD
    memory: 2G                                # slice MemoryMax
    cpus: "2"                                 # slice CPUQuota (200%)
  timeout: 30m
```

`PROBAVI_SSH_TARGET=drill@host` in the environment selects the target.
Params stay evidence-safe (paths and caps only). No `image` param exists;
its absence is what the provider *is*.

## 8. Open questions (maintainer decides before approval)

1. **Provider name**: `bare` | `sshhost` | `remotehost`. (`ssh` alone is
   confusing since remote Docker also rides SSH.)
2. **Privilege model default**: system systemd-run via polkit rule vs
   `--user` mode with lingering (weaker caps on some setups, but no
   polkit configuration to explain).
3. **Engine addressing default**: unix-socket-first (no port collisions,
   nothing on TCP at all) vs loopback-port-first (closer to what the
   existing adapters emit in `connection`). Unix-first likely requires a
   small, engine-side choice in each adapter — to be confirmed against
   the protocol before any protocol change is proposed (none is expected:
   `connection.host` already carries what the adapter decides).
4. **Minimum systemd version** to require and probe for.

## 9. Rejected alternatives

- **Raw process groups (`setsid` + `pkill`)**: no resource caps, no
  reliable kill-the-tree (double-fork escapes process groups), no
  deadline backstop without a hand-rolled watchdog. systemd's cgroup
  tree does all three with boring, universally present tooling.
- **A Go SSH library**: violates the CLI-not-SDK rule, reimplements the
  operator's ssh config/agent/known_hosts handling, and adds a large
  dependency to a trust product.
- **An agent daemon on the target**: explicit project non-goal
  (AGENTS.md §2.4); everything above works over plain ssh exec.
- **Shipping engine binaries to the target** (solving version matching
  by upload): a supply-chain and platform-matrix nightmare; version
  alignment stays an operator duty, honestly documented.
