//go:build !darwin

package trajectory

import "os"

// The shipped app is macOS-only. Other platforms keep their native identity
// for fixture tooling; they cannot claim the macOS persistent-volume contract.
func persistentFileIdentity(_ *os.File, info os.FileInfo) (string, error) {
	return statIdentity(info), nil
}
