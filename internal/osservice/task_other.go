//go:build !windows

package osservice

import "errors"

func newTaskController(Options) (platformController, error) {
	return nil, errors.New("scheduled tasks are available only on Windows")
}
