package ledger

import (
	"os"
	"path/filepath"
)

// appendRaw writes a line straight into the ledger, so a test can plant a
// corrupt one and check that Load steps over it.
func appendRaw(line string) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}
