//go:build !unix

package workspace

// LockRepository is a no-op where flock is unavailable: each window still
// refuses a second run of its own.
func LockRepository(string) (func(), error) {
	return func() {}, nil
}
