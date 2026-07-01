package microvm

import "errors"

// Typed sentinel errors. Callers branch on these with errors.Is; never match
// on string content. Add detail by wrapping: fmt.Errorf("detail: %w", sentinel).
var (
	// ErrInvalidOption is returned when a functional option receives an invalid value.
	ErrInvalidOption = errors.New("microvm: invalid option")
	// ErrInvalidSource is returned when a build source cannot be classified as a
	// supported transport (local path, s3://, https://) or carries credentials.
	ErrInvalidSource = errors.New("microvm: invalid source")
	// ErrSourceTooLarge is returned when a staged build context exceeds a
	// configured size or file-count cap.
	ErrSourceTooLarge = errors.New("microvm: source exceeds size cap")
	// ErrTokenExpired is returned when the shell auth token has expired.
	ErrTokenExpired = errors.New("microvm: shell token expired")
	// ErrVMProvisionFailed is returned when a MicroVM fails to reach RUNNING.
	ErrVMProvisionFailed = errors.New("microvm: vm failed to reach running")
	// ErrImageBuildFailed is returned when a MicroVM image build fails or times out.
	ErrImageBuildFailed = errors.New("microvm: image build failed")
	// ErrImageNotFound is returned when a requested MicroVM image does not exist.
	ErrImageNotFound = errors.New("microvm: image not found")
	// ErrCapabilityMismatch is returned when an existing image is reused but its
	// OS capabilities differ from those requested, so reuse would silently grant
	// or drop privilege. Rebuild (Force) to resolve.
	ErrCapabilityMismatch = errors.New("microvm: image capabilities differ from request")
	// ErrConnClosed is returned when the WebSocket connection is closed unexpectedly.
	ErrConnClosed = errors.New("microvm: connection closed")
	// ErrTimeout is returned when an operation exceeds its deadline.
	ErrTimeout = errors.New("microvm: operation timed out")
	// ErrTerminated is returned when an operation is attempted on a terminated sandbox.
	ErrTerminated = errors.New("microvm: sandbox terminated")
)
