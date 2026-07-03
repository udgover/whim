// Package microvm spawns and controls ephemeral AWS Lambda MicroVMs — fast,
// throwaway Firecracker sandboxes with an interactive shell, command exec, file
// transfer, and warm suspend/resume.
//
// # Quick start
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	mgr := microvm.NewFromConfig(cfg)
//
//	sb, err := mgr.Launch(ctx, imageARN, microvm.WithTTL(25*time.Minute))
//	if err != nil { ... }
//	defer sb.Terminate(ctx)
//
//	err = sb.Shell(ctx, microvm.ShellIO{In: os.Stdin, Out: os.Stdout})
//
// # Surface
//
// Manager owns images and VM lifecycle within one credential context:
// BuildImage/EnsureImage/GetImage/ListImages/DeleteImage, Launch, Attach (an
// existing RUNNING or SUSPENDED VM by id — SUSPENDED is resumed transparently
// before the handle is returned), List/GC (whim-owned VMs), Suspend/Resume,
// Terminate.
//
// Sandbox is a handle to one VM: Shell, Exec (combined output + exit code), Put,
// Get, Suspend, Resume, Terminate (idempotent). Errors are typed sentinels
// matched with errors.Is (ErrInvalidOption, ErrImageNotFound,
// ErrVMProvisionFailed, ErrConnClosed, ErrTimeout, ErrTerminated).
//
// # Credential injection
//
// The library never resolves ambient credentials. Pass an aws.Config built by
// the caller (IRSA, ECS task role, aws-vault, an assumed role, etc.) to
// NewFromConfig. For testing, use NewWithAPI to inject a mock implementation of
// the internal AWS interface.
//
// # Security & invariants
//
//   - Shell auth tokens are never logged.
//   - Every Launch sets a server-side TTL (default 25m, max 8h) as a cleanup
//     backstop; Launch refuses a missing/oversized TTL.
//   - List/GC scope to VMs launched from account-owned microvm-images only —
//     never AWS-managed base images or other accounts' VMs. (Microvms can't be
//     tagged, so ownership is derived from the image ARN.)
//   - Get extracts archives with path-traversal and symlink-escape guards.
//
// # v0.1 limitations
//
// Exec returns combined stdout+stderr (a pty merges them). An established
// interactive shell is NOT bounded by the ~30-minute shell-token lifetime —
// live testing confirmed the token is checked only at connect, never
// re-validated — so its actual lifetime is the VM's own TTL/idle policy.
// Reconnecting still yields a fresh shell, not continuity of the old one (no
// session-state reconnect). Put/Get hold the whole payload in memory (256 MiB
// cap). Terminal resize is accepted but not yet forwarded.
package microvm
