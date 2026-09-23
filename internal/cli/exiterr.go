package cli

import (
	"errors"
	"os/exec"
)

// asExitError unwraps err into target, reporting whether it matched.
func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}
