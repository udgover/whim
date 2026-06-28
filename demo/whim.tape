# whim README demo, single pane  ->  assets/demo.gif
#
# Shell-led tour of the build-once → launch → throwaway-shell story, recorded
# against a REAL AWS account with the account ID masked in-frame. The hero beat
# is the interactive root shell: uname / os-release / whoami typed live at the
# `λ $` prompt, then Ctrl-] to disconnect (the VM is torn down). Then the image
# we built, what's live (a held VM), an exec into it, and a gc that reaps it —
# a clean loop end.
#
# Account masking is real: `export WHIM_REDACT_ACCOUNT=1` (in the Hide preamble
# below) wraps whim's own output so every account ID — the `init` line, the
# `whim-artifacts-<acct>-…` bucket, and every ARN — prints as `<ACCOUNT_ID>`.
# Remote command output (uname / os-release / whoami) is user data and is shown
# verbatim, never redacted.
#
# Prereqs (see demo/README.md):
#   - vhs installed              https://github.com/charmbracelet/vhs
#   - the CURRENT whim on PATH:  go install ./cmd/whim
#     (needs `whim ps` + WHIM_REDACT_ACCOUNT — both newer than any v0.1.0 binary)
#   - whim already initialised:  whim init   (the tape re-runs the cached path)
#   - a clean slate:             whim gc --yes   (so the held VM is the only one)
#   - AWS creds in the environment for a MicroVM-enabled region (e.g. us-east-1)
#
# Record from the repo root:   vhs demo/whim.tape      # writes assets/demo.gif

Output assets/demo.gif

Require whim

Set Shell "bash"
Set FontSize 15
Set Width 1300
Set Height 860
Set Padding 20
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"

# --- Hide preamble: env + a held VM, all off-camera ----------------------------
# Mask the account everywhere whim prints it; give the host a clean prompt; and
# hold a live VM in the background (a separate VM from the ephemeral shell below)
# so `ps`/`exec` later have a real target. Capture its id into $VM, then clear so
# the recording opens on a clean prompt.
Hide
Type "export PS1='$ '" Enter
Type "export WHIM_REDACT_ACCOUNT=1" Enter
Type "whim run -- sleep 900 >/dev/null 2>&1 & disown" Enter
Type "for i in $(seq 30); do VM=$(whim ps -q | head -1); [[ -n $VM ]] && break; sleep 1; done" Enter
Type "clear" Enter
Show

# --- build-once: the one-time bootstrap, cached so it returns in seconds --------
Type "whim init"
Sleep 400ms
Enter
Sleep 4s

# --- the hero: an interactive throwaway root shell -----------------------------
Type "whim"
Sleep 400ms
Enter
Sleep 3s
# Launch chrome is on screen ("Connected … in ~2s"). Swap the guest prompt for a
# clean `λ $` and clear, off-camera, so the hero beat opens on the prompt.
Hide
Type "export PS1='λ $ '" Enter
Type "clear" Enter
Sleep 300ms
Show

Type "uname -a"
Sleep 400ms
Enter
Sleep 2s
Type "cat /etc/os-release"
Sleep 400ms
Enter
Sleep 2500ms
Type "whoami"
Sleep 400ms
Enter
Sleep 1500ms
# Ctrl-] is the reserved local disconnect → "Disconnected." → the VM is gone.
Ctrl+]
Sleep 2500ms

# --- what we built, what's live, reach in, reap --------------------------------
Hide
Type "clear" Enter
Show

Type "whim image ls"
Sleep 400ms
Enter
Sleep 3s
Type "whim ps"
Sleep 400ms
Enter
Sleep 3s
Type "whim exec $VM -- whoami"
Sleep 400ms
Enter
Sleep 2500ms
Type "whim gc --yes"
Sleep 400ms
Enter
Sleep 4s
