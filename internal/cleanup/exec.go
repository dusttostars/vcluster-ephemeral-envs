package cleanup

import (
	"os/exec"
)

// execCommand wraps exec.CommandContext for testability.
var execCommand = exec.CommandContext
