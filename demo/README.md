# demo/

The README hero GIF — a real, shell-led tour of whim recorded with
[VHS](https://github.com/charmbracelet/vhs). `whim.tape` writes
`assets/demo.gif`.

It tells the build-once → launch → throwaway-shell story in one ~30s loop:

1. **`whim init`** — the one-time bootstrap, run against the cached image so it
   returns in seconds (not a real ~3-min build) while still reading as build-once.
2. **`whim`** — the hero: an interactive root shell in a Firecracker MicroVM.
   `uname -a` (aarch64 / Graviton), `cat /etc/os-release` (Amazon Linux 2023),
   and `whoami` (root) are typed **live** at the `λ $` prompt, then **Ctrl-]**
   disconnects and the VM is torn down.
3. **`whim image ls`** / **`whim ps`** — the image we built and the live VM.
4. **`whim exec $VM -- whoami`** — reach into an existing VM (→ root).
5. **`whim gc --yes`** — reap it; clean loop end.

## Account masking is real (`WHIM_REDACT_ACCOUNT`)

The tape `export WHIM_REDACT_ACCOUNT=1` in its `Hide` preamble. That gates a
redacting writer wrapping whim's own output, so every place whim prints the
12-digit account — the `init` line, the `whim-artifacts-<acct>-<region>` bucket,
and every ARN — shows as `<ACCOUNT_ID>`. It is **not** `sed` over the terminal:
real commands stay on screen, and remote command output (`uname` / `os-release`
/ `whoami`) is user data, shown verbatim and never redacted. The feature is
useful well beyond the demo — screenshares, bug reports, blog posts.

Verify the coverage before recording:

```bash
WHIM_REDACT_ACCOUNT=1 whim image ls --json | grep -E '[0-9]{12}'   # → empty
```

## Prereqs

- [`vhs`](https://github.com/charmbracelet/vhs) (plus its `ttyd` + `ffmpeg`).
- **The current `whim` on `$PATH`** — `go install ./cmd/whim`. The tape uses
  `whim ps` and `WHIM_REDACT_ACCOUNT`, both newer than any pre-tag binary, so a
  stale install will fail. Confirm with `whim ps --help` and the `grep` above.
- **whim already initialised** — `whim init` once, so the tape's `init` hits the
  cached path and the held VM can launch from the default image.
- **A clean slate** — `whim gc --yes` first, so the only VM `ps`/`exec`/`gc` see
  is the one the tape holds (the preamble captures its id with
  `whim ps -q | head -1`).
- **AWS credentials** in the environment for a MicroVM-enabled region (e.g.
  `us-east-1`).

## Record

```bash
vhs demo/whim.tape        # writes assets/demo.gif
```

The preamble backgrounds a `whim run -- sleep 900` and waits (off-camera) for it
to reach `RUNNING`, so `ps`/`exec` have a live target; `gc --yes` reaps it at the
end. Note that held VM is **separate** from the ephemeral shell VM (which Ctrl-]
already terminated).

## Tuning

The `Sleep` beats are guides — tune to a real run:

- If the shell's launch runs longer than the ~3s budgeted after `Type "whim"`,
  the `Connected … in ~Ns` line may not land before the prompt swap. Raise that
  `Sleep`, or wrap the whole launch in `Hide`/`Show` to cut the wait entirely.
- The `λ $` prompt is set with `export PS1` inside the guest, off-camera. If a
  guest dotfile overrides it, the recording shows the default prompt — honest,
  just less pretty.
- Give `whim ps` enough lead after the disconnect for the ephemeral VM to leave
  the list, so only the held VM shows.
