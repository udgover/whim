# whim

Ephemeral AWS Lambda MicroVM shells — a Go library (`microvm`) and CLI (`whim`).

Spin up a throwaway root shell in a Firecracker MicroVM in ~2 seconds, run
something, and let it disappear.

![whim: build once, launch a throwaway root shell in ~2s, then reap it](assets/demo.gif)

> Recorded against a real AWS account — the account ID is masked in-frame via
> `WHIM_REDACT_ACCOUNT`. `whim init` (cached), then the hero beat: an interactive
> root shell running `uname` / `os-release` / `whoami` at the `λ $` prompt, `Ctrl-]`
> to disconnect, and `image ls` / `ps` / `exec` / `gc` to list, reach into, and reap
> a live VM.

```
whim init            # one-time: build the default sandbox image (~3 min)
whim                 # drop into a throwaway root shell
whim run -- pytest   # spawn → run → stream output → propagate exit code → terminate
```

## How it works (build-once, run-many)

Lambda MicroVMs run from a **prebuilt image**, so there's a one-time bootstrap:

1. `whim init` provisions an S3 build bucket + IAM build role, then builds the
   default image (AL2023 + `tar`/`gzip`) and caches its ARN in
   `~/.config/whim/config.json`. This takes a couple of minutes.
2. After that, every `whim` / `whim run` launches a fresh VM from that cached
   image in ~2s, attaches over a WebSocket, and terminates on exit.

Every VM is launched with a **server-side TTL** (default 25m, max 8h) as a
cleanup backstop, so nothing is left running if a client dies.

## Requirements

- An AWS account enabled for Lambda MicroVMs, in a supported region (e.g. `us-east-1`)
- AWS credentials — `aws login`, `aws-vault`, an assumed role, or the environment
- Go 1.24+

## Install

```bash
go install github.com/udgover/whim/cmd/whim@latest   # → $(go env GOPATH)/bin
aws login
whim preflight-check      # verify creds + permissions
whim init                 # build the default image
```

For development, build from a clone instead:

```bash
git clone https://github.com/udgover/whim && cd whim
go install ./cmd/whim
```

## CLI

| Command | What it does |
|---|---|
| `whim` / `whim shell` | Launch a VM and attach an interactive root shell. **Ctrl-]** disconnects (every other key, incl. Ctrl-C, goes to the remote pty). |
| `whim run -- <cmd…>` | Launch → run → stream combined output → propagate the exit code → terminate. |
| `whim run -d` [`--idle <dur>`] [`--suspend-after <dur>`] [`--auto-resume`] | Launch a **persistent box** that outlives the process; prints the bare id and returns (see "Persistent boxes" below). |
| `whim exec <id> -- <cmd…>` | Run a command in an existing VM (auto-resumes if suspended). |
| `whim exec -it <id>` | Attach an interactive shell to an existing/suspended VM; **Ctrl-]** detaches without terminating it. Takes no trailing command. |
| `whim put <id> <local> <remote-dir>` / `whim get <id> <remote> <local-dir>` | Copy files/dirs in or out (tar.gz, binary-safe, `scp -r`/`docker cp` semantics; auto-resumes if suspended). |
| `whim ps` [`-q`] [`--json`] | List your whim VMs — running/pending/suspended, state-labeled (like `docker ps`). |
| `whim gc` [`--older-than <dur>`] [`--yes`] | Terminate your whim-**owned** VMs in bulk (confirms unless `--yes`). |
| `whim suspend <id>` / `whim resume <id>` | Pause/restart a VM (disk + memory preserved). |
| `whim rm <id…>` | Terminate VMs **by id** — like `suspend`/`resume`/`exec`, no ownership check (contrast `gc`). Idempotent on an already-gone id. |
| `whim image ls` [`-q`] [`--json`] / `whim image rm <name…>` | Manage built images. |
| `whim build <source> --name <n>` [`--egress public\|none`] [`--egress-connector <arn|name>`] [`--egress-auto-provision`] [`--force`] [`--context-subdir <p>`] [`--json`] | Build a custom image from a local dir/Dockerfile, `s3://`/`https://` archive, or `github.com/org/repo@ref`; caches the ARN under `--name`. See "Building custom images" for the full `--egress-*` flag set. |
| `whim init` [`--image-name <n>`] [`--force`] / `whim preflight-check` / `whim version` | Bootstrap, checks, version. |

Global flags: `--region`, `--profile`. `shell`/`run` also take `--image <name|arn>`
and `--ttl <dur>`. `run`/`exec`/`put`/`get` exit **125** for whim-level failures
(distinct from a remote command's own code).

## Building custom images (`whim build`)

`whim init` builds the *default* image; `whim build` builds *custom* images from
your own source and caches each one by `--name` (the name is the build/reuse
key) alongside the default in `~/.config/whim/config.json`. It reuses the same
bootstrap (artifact bucket, build role, managed base image) as `init`.

```bash
whim build ./app          --name whim-app                       # local build context (Dockerfile at its root)
whim build ./Dockerfile   --name whim-min  --egress none --egress-connector whim-no-egress
whim build ./app --name whim-isolated --egress none --egress-auto-provision \
  --egress-operator-role arn:aws:iam::123456789012:role/whim-network-operator
whim build s3://my-bucket/app.zip            --name whim-s3 --json
whim build https://example.com/app.zip       --name whim-https
GITHUB_TOKEN=… whim build github.com/org/repo@<full-sha> --name whim-repo --force
```

Sources: a local directory or Dockerfile, an `s3://bucket/key`, an
`https://host/path` (zip archive or raw Dockerfile), or **GitHub shorthand**
`github.com/org/repo@ref` / `git+https://github.com/org/repo@ref`. GitHub
shorthand is CLI-only — it lowers to an HTTPS archive download (no `git` needed);
the `microvm` library transports are local, `s3://`, and `https://` only.

- **`--name` is required** and is the cache key: a second `whim build --name X`
  reuses the existing image `X` unless you pass `--force` (which deletes and
  rebuilds). Name-as-key means a changed source does **not** rebuild on its own.
- **`--egress public|none`** records the image's intended egress connector set
  and Whim mirrors it when launching VMs. AWS associates network connectors at
  `run-microvm` time, and public internet egress is the service default.
  `public` uses AWS's managed `INTERNET_EGRESS` connector. `none` means
  **`NO_PUBLIC_EGRESS`** — direct public IP and public-hostname HTTPS
  connections fail — **not** a native `NO_EGRESS` mode; AWS does not document
  one, and Whim never claims otherwise (see Security model below for exactly
  what's tested). `--egress none` requires exactly one of:
  - **`--egress-auto-provision`** — Whim creates (or safely reuses) a
    dedicated VPC, subnet, route table, security group, and Lambda Core
    connector with no public/default route and zero security-group egress
    rules. Requires **`--egress-operator-role`**: an existing IAM role ARN
    trusting `lambda.amazonaws.com` with `AWSLambdaNetworkConnectorOperatorPolicy`
    attached. **Whim never creates this role itself** — create it once and
    reuse it. Override naming/CIDRs with `--egress-resource-prefix` (default
    `whim`), `--egress-vpc-cidr` (default `10.242.99.0/24`), and
    `--egress-subnet-cidr` (default `10.242.99.0/25`) if the defaults
    collide with a network you already use.
  - **`--egress-connector <arn|name>`** — point at a connector you already
    manage. Whim resolves its live subnet/route-table/security-group topology
    and rejects it unless every route is VPC-local and every attached security
    group has zero outbound rules.
  - **`--egress-subnet`/`--egress-security-group` with an explicit
    `--egress-connector-name`** — an advanced escape hatch: Whim creates only
    the Lambda Core connector, against subnets/security groups you already
    own, with no VPC creation. Whim verifies that an existing same-named
    connector matches the supplied subnet/security-group IDs, then applies the
    same no-public-route/zero-egress validation. `--egress-connector-name` must
    be given explicitly here — its default value is never used silently.

  Combining more than one of the above is rejected, as is any flag that
  belongs to a mode you didn't select (e.g. `--egress-connector` with
  `--egress-vpc-cidr`) — they would otherwise be silently ignored.
  **`--egress-strict-dns` is not implemented**: DNS resolution is not
  blocked under `--egress none` (see Security model).

  **The image build itself egresses through whatever `--egress` resolves
  to.** A Dockerfile with network-dependent `RUN` steps (package installs,
  etc.) will fail to build under `--egress none` against an isolated
  connector — build those images under `--egress public` first, then launch
  them against the isolated connector at run time instead.

  For the AWS requirements (operator role, caller permissions, service-linked
  role) and how to grant them following least privilege, see
  [docs/no-public-egress-setup.md](docs/no-public-egress-setup.md). For a
  repeatable manual test of every `--egress*` switch end-to-end, see
  [docs/no-public-egress-test-protocol.md](docs/no-public-egress-test-protocol.md).
- **`--context-subdir <p>`** descends into a subdirectory before locating the
  `Dockerfile` (also strips a single wrapping top-level dir from forge archives).
- **`--json`** prints one redacted object `{name, arn, source, cached, egress}`
  plus `egress_connector` and the validated `egress_resource_group` topology
  (`vpc_id`, subnet/route-table/security-group IDs, and `resource_group` for
  auto-provisioned resources) for `--egress none`, and suppresses progress
  chatter.
- **GitHub auth:** set `GITHUB_TOKEN` for private repos — it is sent only as a
  request header, never placed in a URL, printed, logged, or written to config.
  A full commit SHA ref is an immutable source identity; branches/tags are moving.

**Source safety & limits.** Every source — local, S3, and HTTPS alike — is
validated and normalized before any image build: archives are re-rooted, and
absolute/`..`/duplicate paths and root-escaping symlinks are rejected. Caps are
**256 MiB compressed, 1 GiB uncompressed, and 10,000 files**. A `.dockerignore`
at the context root is honored for a **documented subset**: blank lines, `#`
comments, `!` negation, leading-`/` anchoring, trailing-`/` directory matches,
and `*`/`?` single-segment globs. Unsupported constructs (`**` cross-segment
globs and `[…]` character classes) are **rejected** rather than mis-applied, so a
wrong ignore rule can't silently ship excluded files.

**Not in v0.1:** private-ECR base images (the build role's IAM is intentionally
not widened until that path is validated) — `whim build` builds on the managed
base image, same as `init`.

## Privileged shells (`--privileged`)

A whim shell is **root**, but under a restricted Linux capability set — so `mount`,
network namespaces, `unshare`, and eBPF are all denied (`EPERM`) even as uid 0.
`--privileged` grants the image AWS's `additionalOsCapabilities: ["ALL"]`, which
lifts the effective set to *all* capabilities. It is **opt-in**: default images
stay minimal-cap and privilege is never granted implicitly.

```bash
whim --privileged                 # throwaway shell that can mount, netns, run eBPF
whim run --privileged -- mount -t tmpfs none /mnt   # one-shot privileged command
```

`whim --privileged` launches the well-known **`whim-privileged`** image, building
it on first use (~2-3 min, like `whim init`) and caching its ARN alongside your
other images. It ships the tooling to *use* the caps — `util-linux` (mount,
`unshare`), `iproute` (`ip netns`), `e2fsprogs` — since capabilities unlock
syscalls, not binaries. `--privileged` and `--image` are mutually exclusive.

To bake privilege into a **custom** image instead, use `whim build --privileged`:

```bash
whim build ./app --name whim-app --privileged     # your source + ALL capabilities
```

Capabilities are fixed at **build time** and inherited by every VM launched from
the image. `ALL` is the only value AWS supports today. Elevated capabilities are
applied **within the VM's isolation boundary** — per AWS, they do not affect the
host or other MicroVMs.

## Persistent boxes (`run -d`, `exec -it`, `rm`)

`whim shell`/`whim run` are disposable — the VM terminates when you disconnect or the
command finishes. `whim run -d` instead launches a **persistent box**: a VM that
outlives the process, which you reconnect to later with `whim exec -it`.

```bash
whim run -d --idle 15m --suspend-after 2h    # prints the id, returns immediately
whim exec -it microvm-abc123                 # attach an interactive shell
# ...work, then Ctrl-] to detach — the box keeps running...
whim exec -it microvm-abc123                 # reconnect later: fresh shell, disk intact
whim rm microvm-abc123                       # done — terminate it
```

- **`-d`/`--detach`** launches without attaching or terminating; the printed id is
  bare (no timestamp) so it's scriptable: `id=$(whim run -d)`.
- **`--idle <dur>` / `--suspend-after <dur>` / `--auto-resume`** configure the box's
  idle policy (both durations require `-d`). While suspended you pay storage, not
  compute; `exec`/`put`/`get`/`exec -it` all resume it transparently on first use.
- **`whim exec -it <id>`** attaches an interactive shell to a running *or suspended*
  box. Unlike `docker exec -it`, it takes **no trailing command** — the shell endpoint
  always starts a fresh shell, with no way to select a program over that connection;
  run your command once you're attached instead.
- **`whim rm <id…>`** terminates by id (like `suspend`/`resume`/`exec` — no ownership
  check; see Security model below). Idempotent on an already-gone id, and a failure on
  one id in a list doesn't stop the rest from being attempted.

**What persists across a detach/reconnect, and what doesn't:**
- ✅ Files on disk, and any background process that's still running.
- ❌ Your terminal state — the open editor, shell history-in-progress, current
  directory. Reconnecting always gives you a **fresh shell** (same reconnect
  semantics `whim shell` already has — see v0.1 limitations below). Run `tmux`
  inside the box if you want the session itself, not just the disk, to survive.

**The idle policy's real behavior — read this before relying on it for cost control.**
Both directions were verified live, and one is the opposite of what you'd guess:
- A box you've **detached** from (no attached shell) with a background job running
  *does* suspend after `--idle` with no traffic — and the job actually freezes, not
  just billing. It resumes (and the job continues) the next time anything touches it.
- A box with an **attached shell — even sitting idle at the prompt, sending
  nothing — does NOT suspend.** Merely holding the connection open counts as
  traffic. So `whim exec -it <id>` left attached while you walk away keeps the box
  **running and billed until `--ttl`, detach, or `rm`** — `--idle` only protects you
  once you actually detach (Ctrl-]).
- A session's actual lifetime is governed by the VM's own `--ttl` (hard cap, ≤8h) and
  its idle policy — **not** by any client-side token-refresh mechanism. There isn't
  one: an established shell connection is unaffected by its underlying auth token
  expiring in the background.

## Library

The `microvm` package is the product; the CLI is a thin client. It is
**credential-injection-only** — it never resolves ambient credentials; you pass
an `aws.Config` you built (so it composes with IRSA, ECS task roles, aws-vault,
assumed roles, etc.).

```go
import "github.com/udgover/whim/microvm"

cfg, _ := config.LoadDefaultConfig(ctx)          // caller owns credential resolution
mgr := microvm.NewFromConfig(cfg)

sb, err := mgr.Launch(ctx, imageARN, microvm.WithTTL(25*time.Minute))
if err != nil { /* … */ }
defer sb.Terminate(ctx)

res, _ := sb.Exec(ctx, []string{"sh", "-c", "echo hi"})  // combined output + exit code
sb.Shell(ctx, microvm.ShellIO{In: os.Stdin, Out: os.Stdout})
sb.Put(ctx, "./src", "/opt"); sb.Get(ctx, "/var/log", "./logs")
sb.Suspend(ctx); sb.Resume(ctx)
mgr.List(ctx); mgr.GC(ctx, microvm.GCFilter{OlderThan: time.Hour})
```

To build a custom image from source, the library takes **explicit build
inputs** — there is no ambient discovery (the CLI owns that). The caller
supplies the artifact bucket, base image, and build role; GitHub shorthand and
default-bootstrap convenience live in the CLI, not here:

```go
arn, err := mgr.BuildFromSource(ctx, "./app", microvm.BuildFromSourceOptions{
    Name:               "whim-app",            // image name = reuse/cache key (required)
    ArtifactBucket:     "my-artifact-bucket",  // caller-owned; staged context uploads here (required)
    BaseImageARN:       baseImageARN,          // managed/base image to build on (required)
    BuildRoleARN:       buildRoleARN,          // role the build assumes to read the staged artifact (required)
    Egress:             microvm.EgressNone,    // connector-backed isolated VPC egress
    EgressConnectorARN: isolatedConnectorARN, // required for EgressNone
    // Force, ContextSubdir, HTTPSHeaders, MaxCompressedBytes, MaxUncompressedBytes …
})
```

`source` resolves to exactly one transport — a local path, `s3://bucket/key`, or
`https://host/path`; `http://`, credential-bearing URL userinfo, and unknown
schemes are rejected.

`isolatedConnectorARN` above can come from your own connector, or from
`Manager.EnsureNoPublicEgressConnector` — the library call the CLI's
`--egress-auto-provision` wraps. It discovers-and-validates a Whim-managed
`NO_PUBLIC_EGRESS` VPC/subnet/route table/security group/connector, reusing
it only after every layer passes validation, and creates one when nothing
safe exists to reuse (never falling back to public egress on failure):

```go
resources, err := mgr.EnsureNoPublicEgressConnector(ctx, microvm.NoPublicEgressSpec{
    OperatorRoleARN: operatorRoleARN, // existing role; Whim never creates it (required to create)
    VPCCIDRBlock:    "10.242.99.0/24", // required to create; no default at the library level
    SubnetCIDRBlock: "10.242.99.0/25",
})
// resources.ConnectorARN, .VPCID, .SubnetIDs, .RouteTableID, .SecurityGroupID
```

For a caller-managed connector, use `ValidateNoPublicEgressConnector` before
building. At launch, pass the recorded result through
`WithExpectedNoPublicEgress`; this verifies the baked image connector and
revalidates the live topology without overriding image metadata.

Errors are typed sentinels (match with `errors.Is`): `ErrInvalidOption`,
`ErrInvalidSource`, `ErrSourceTooLarge`, `ErrImageNotFound`,
`ErrVMProvisionFailed`, `ErrImageBuildFailed`, `ErrConnClosed`, `ErrTimeout`,
`ErrTerminated`. `EnsureNoPublicEgressConnector` additionally wraps
`ErrConnectorMissing`, `ErrConnectorNotActive`, `ErrResourceNotOwned`,
`ErrPublicRoute`, `ErrSecurityGroupEgress`, `ErrTopologyMismatch`, and
`ErrOperatorRoleInvalid` in a `*NoPublicEgressViolation` (or
`*PartialNoPublicEgressCreationError` for a mid-creation failure) — see
Troubleshooting above for what each means. For tests, inject a mock via
`NewWithAPI`.

## Security model

The VM runs **your code as root**, reachable only through an authenticated
WebSocket. whim's guarantees:

- **`--egress none` is `NO_PUBLIC_EGRESS`, live-tested, not just documented.**
  A MicroVM launched from a `--egress none` image cannot reach a direct
  public IP or a public hostname over HTTPS: both time out at TCP connect,
  verified by the gated `TestIntegration_NoPublicEgress` flow against a real
  Whim-managed connector. **DNS resolution
  is not blocked**: `getent hosts`/`nslookup` inside the VM still resolves
  public hostnames to real IPs; only the resulting connection fails. Security
  groups and network ACLs cannot filter traffic to AWS's own DNS resolver, so
  this is `NO_PUBLIC_EGRESS`, never claimed as AWS's undocumented (and
  unsupported) `NO_EGRESS`, and never silently claimed as blocking DNS.
  MicroVM metadata for a `--egress none` launch always reports the
  customer-managed connector, never `INTERNET_EGRESS`.
- **Shell tokens are never logged** (the `X-aws-proxy-auth` value lives only in
  the request header).
- **Injection-only credentials** — `microvm` never sources ambient credentials.
- **Always a TTL** — no VM is launched without one (≤ 8h).
- **Privilege is opt-in** — default images run with a restricted capability set;
  `--privileged` (all OS capabilities) must be asked for explicitly, and its
  reach is confined to the VM's isolation boundary (host and other MicroVMs are
  unaffected).
- **Ownership-scoped cleanup** — `ps`/`gc` only ever touch VMs launched from
  **your own account's microvm-images**; AWS-managed base images and other
  accounts' VMs are never listed or reaped. (Microvms can't be tagged, so
  ownership is derived from the image ARN — see the caveat below.) `gc` confirms
  unless `--yes`.
- **Path-traversal & symlink guards** on `get` — a compromised VM cannot write
  outside the destination via a crafted tar (absolute/`..`/escaping-symlink
  entries are rejected; setuid/setgid bits are stripped).
- Remote paths are shell-quoted (no command injection via filenames).

**Ownership model:** only the *heuristic bulk* commands `ps`/`gc` are scoped to
whim-owned VMs. The **id-targeted** commands (`exec`, `put`, `get`, `suspend`,
`resume`, `rm`) act on **any** VM in your account that you explicitly name —
like `ssh <host>`, naming the target is the authorization. In a shared account,
an explicit `whim rm <foreign-id>` could **terminate** someone else's workload.

## Troubleshooting `--egress-auto-provision`

`--egress-auto-provision` fails closed rather than silently falling back to
public egress or "fixing" a resource it doesn't fully trust. The error names
which check failed:

- **`no-public-egress connector not found`** — nothing exists yet under the
  expected connector name. This is the normal trigger for creation, not an
  error unless creation itself then also fails.
- **`... exists but is not ACTIVE`** — a same-named connector exists but is
  `PENDING` (Whim polls this to `ACTIVE` automatically and proceeds), or
  `FAILED`/`INACTIVE`/`DELETING`/`DELETE_FAILED` (Whim does not retry these —
  inspect it with `aws lambda-core get-network-connector --identifier <name>`
  and delete/recreate it manually).
- **`resource is not tagged as Whim-managed`** — a resource matching the
  expected name/ID exists but lacks Whim's `ManagedBy=whim` tag. Whim never
  reuses a resource it doesn't recognize as its own, even if its shape looks
  safe.
- **`resource topology does not match expected no-public-egress shape`** —
  drift: a connector, route table, or NACL exists but is associated with
  different subnets/security groups than Whim expects. Whim never repairs
  this automatically or creates a second resource group under the same name.
- **`route table has a public or default egress route`** / **`security
  group has an outbound rule`** — the managed route table or security group
  has drifted from the no-public-egress shape (a public/default route, or
  any egress rule at all — even a narrow one). Whim fails closed rather than
  treating a partially-open network as isolated.
- **`operator role is not usable by Lambda Core network connectors`** — your
  `--egress-operator-role` either doesn't exist, doesn't trust
  `lambda.amazonaws.com`, or has no attached/inline policies at all. This
  check cannot fully verify the role's *permissions* (that needs
  `iam:GetPolicyVersion`/`GetRolePolicy`, which Whim doesn't call), so a role
  that passes it can still fail at connector creation with an AWS
  `AccessDenied` if its policy doesn't actually grant
  `ec2:CreateNetworkInterface`. Attach the AWS-managed
  `AWSLambdaNetworkConnectorOperatorPolicy` for the minimum permissions.
- **Partial creation failure** — if creation fails partway (e.g. the VPC and
  subnet were created but security-group creation then failed), the error
  names every resource ID created so far. Whim never rolls these back
  automatically — find and delete them manually; there is no `whim egress
  rm` yet.

Whim never creates the IAM operator role itself: create it once (trust
`lambda.amazonaws.com`, attach `AWSLambdaNetworkConnectorOperatorPolicy`) and
reuse its ARN via `--egress-operator-role` for every `--egress-auto-provision`
build.

## v0.1 limitations

- **Ownership is image-derived, not tagged.** Lambda MicroVMs can't be tagged, so
  `ps`/`gc` treat *any* VM launched from one of your account's microvm-images as
  whim-owned. In the rare case you run another tool that also launches from
  microvm-images, scope `gc` carefully.
- **Exec output is combined** stdout+stderr (a pty merges them); separate streams
  would need a guest agent.
- **Transfers are in-memory**, capped at 256 MiB per `put`/`get`.
- **Reconnect yields a new shell** (disk persists, in-memory shell state does not).
- Terminal **resize isn't forwarded** yet (full-screen apps use the default size).
- **Strict DNS is not implemented ([#7](https://github.com/udgover/whim/issues/7)).** `--egress none` (with or without
  `--egress-auto-provision`) never blocks DNS resolution; `--egress-strict-dns`
  is rejected until this is built and live-verified.
- **Whim never creates the IAM operator role.** `--egress-auto-provision`
  requires an existing `--egress-operator-role`; Whim only validates it
  (trust principal, presence of a policy), never calls `iam:CreateRole`.
- **No cleanup command for auto-provisioned resources yet.** `whim image rm`
  never deletes the VPC/subnet/route table/security group/connector an
  `--egress-auto-provision` build created — delete them manually (see
  Troubleshooting above) until a dedicated command exists.

## Tests

Unit tests are hermetic (no AWS, no network):

```bash
go test ./...
```

Live AWS integration tests are build-tagged and opt-in — they provision and
build real images using your `whim init` artifact bucket and build role:

```bash
WHIM_INTEGRATION=1 go test -tags=integration ./...
```

The local-directory build runs as-is. Set `WHIM_TEST_EGRESS_CONNECTOR` to an
isolated Lambda Core VPC connector ARN to run the `--egress none` integration
test. Set `WHIM_TEST_EGRESS_OPERATOR_ROLE` to an IAM role ARN (trusting
`lambda.amazonaws.com`, `AWSLambdaNetworkConnectorOperatorPolicy` attached) to
run `TestIntegration_NoPublicEgress`, which provisions/reuses a real
no-public-egress resource group via `EnsureNoPublicEgressConnector`, asserts
MicroVM metadata never reports `INTERNET_EGRESS`, and asserts a direct public
IP and an HTTPS public hostname both fail to connect. The same steps are in
[docs/no-public-egress-test-protocol.md](docs/no-public-egress-test-protocol.md).
Set `WHIM_TEST_HTTPS_ZIP` (an `https://` zip whose Dockerfile is at the
root) and `WHIM_TEST_GITHUB=org/repo@<full-sha>` (plus `GITHUB_TOKEN` for
private repos) to exercise the remote-source paths. Override the derived inputs
with `WHIM_TEST_ARTIFACT_BUCKET` / `WHIM_TEST_BUILD_ROLE` /
`WHIM_TEST_BASE_IMAGE`.

## License

Apache 2.0
