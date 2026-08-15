//go:build !darwin

package tool

import (
	"context"
	"fmt"
	"os/exec"
)

// osSandboxPlatformSupported darwin 以外では常に false。
// os_sandbox: auto を「no-op と等価」に解決するのはこの値による
func osSandboxPlatformSupported() bool {
	return false
}

func wrapWithOSSandbox(_ context.Context, _ []string, _ string, _ []string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("os_sandbox: darwin 以外では利用できません")
}
