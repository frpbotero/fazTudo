//go:build !unix

package workspace

import "context"

// LockRepository is a no-op where flock is unavailable: each window still
// refuses a second run of its own.
func LockRepository(string) (func(), error) {
	return func() {}, nil
}

// LockStackSlot is a no-op where flock is unavailable.
func LockStackSlot(context.Context, func()) (func(), error) {
	return func() {}, nil
}
