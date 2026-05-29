//go:build codecfull

package codecfull

import (
	"os"
	"os/exec"
	"testing"
)

func testCommand(t *testing.T, testName string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^"+testName+"$")
	command.Env = append([]string(nil), os.Environ()...)
	return command
}
