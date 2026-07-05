package githubauth

import (
	"errors"
	"syscall"
)

func isConnResetOrRefused(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}
