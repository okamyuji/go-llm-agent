//go:build !darwin && !linux

package agent

import "os/exec"

// configureHookProcess keeps CommandContext's direct-process cancellation.
// WaitDelay in runHook bounds inherited pipe waits, while descendant process
// termination remains platform-dependent.
func configureHookProcess(_ *exec.Cmd) {}
