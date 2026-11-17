//go:build !darwin && !linux

package maintenance

import "errors"

var ErrBusy = errors.New("data is in use")
var ErrIncomplete = errors.New("incomplete maintenance operation")

const IncompleteMarker = ".maintenance-incomplete"

type Lock struct{}

func Acquire(string, string) (*Lock, error) {
	return nil, errors.New("maintenance locks require Linux or macOS")
}
func AcquireForRestore(a, b string) (*Lock, error) { return Acquire(a, b) }
func (*Lock) Close() error                         { return nil }
