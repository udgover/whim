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
| `whim exec <id> -- <cmd…>` | Run a command in an existing VM. |
| `whim put <id> <local> <remote-dir>` / `whim get <id> <remote> <local-dir>` | Copy files/dirs in or out (tar.gz, binary-safe, `scp -r`/`docker cp` semantics). |
| `whim ps` [`-q`] [`--json`] | List your whim VMs — running/pending/suspended, state-labeled (like `docker ps`). |
| `whim gc` [`--older-than <dur>`] [`--yes`] | Terminate your whim VMs (confirms unless `--yes`). |
| `whim suspend <id>` / `whim resume <id>` | Pause/restart a VM (disk + memory preserved). |
| `whim image ls` [`-q`] [`--json`] / `whim image rm <name…>` | Manage built images. |
| `whim init` [`--image-name <n>`] [`--force`] / `whim preflight-check` / `whim version` | Bootstrap, checks, version. |

Global flags: `--region`, `--profile`. `shell`/`run` also take `--image <name|arn>`
and `--ttl <dur>`. `run`/`exec`/`put`/`get` exit **125** for whim-level failures
(distinct from a remote command's own code).

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

Errors are typed sentinels (match with `errors.Is`): `ErrInvalidOption`,
`ErrImageNotFound`, `ErrVMProvisionFailed`, `ErrConnClosed`, `ErrTimeout`,
`ErrTerminated`. For tests, inject a mock via `NewWithAPI`.

## Security model

The VM runs **your code as root**, reachable only through an authenticated
WebSocket. whim's guarantees:

- **Shell tokens are never logged** (the `X-aws-proxy-auth` value lives only in
  the request header).
- **Injection-only credentials** — `microvm` never sources ambient credentials.
- **Always a TTL** — no VM is launched without one (≤ 8h).
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
`resume`) act on **any** VM in your account that you explicitly name — like
`ssh <host>`, naming the target is the authorization. In a shared account, an
explicit `whim suspend <foreign-id>` could affect someone else's workload.

## v0.1 limitations

- **Ownership is image-derived, not tagged.** Lambda MicroVMs can't be tagged, so
  `ps`/`gc` treat *any* VM launched from one of your account's microvm-images as
  whim-owned. In the rare case you run another tool that also launches from
  microvm-images, scope `gc` carefully.
- **Exec output is combined** stdout+stderr (a pty merges them); separate streams
  would need a guest agent.
- **Interactive shells last ~30 min** (the shell-token lifetime); `--ttl` above
  that doesn't extend the session (no reconnect in v0.1).
- **Transfers are in-memory**, capped at 256 MiB per `put`/`get`.
- **Reconnect yields a new shell** (disk persists, in-memory shell state does not).
- Terminal **resize isn't forwarded** yet (full-screen apps use the default size).

## License

Apache 2.0
