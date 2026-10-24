package workspace

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// ErrRepositoryBusy means another FazTudo window (process) runs a pipeline in
// the repository: two runs would check out branches over each other.
var ErrRepositoryBusy = errors.New("outra janela do FazTudo já está executando neste repositório")

// lockFile is the per-repository lock shared by every FazTudo process.
func lockFile(repo string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(repo))
	dir = filepath.Join(dir, "faztudo", "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".lock"), nil
}
