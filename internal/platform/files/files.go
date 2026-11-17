// Package files prepares private storage. It does not publish uploaded files.
package files

import (
	"fmt"
	"os"
)

func Prepare(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("prepare file storage: %w", err)
	}
	return nil
}
