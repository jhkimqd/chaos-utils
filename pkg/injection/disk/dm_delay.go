package disk

import (
	"context"
	"fmt"
)

// DmDelayWrapper is a placeholder for a device-mapper delay injector.
// The previous implementation created a dm-delay mapping but never swapped
// the filesystem mount onto it, so it applied zero observable latency.
// Doing the swap correctly requires mount namespace manipulation that is
// not supported in our sidecar container model.
type DmDelayWrapper struct{}

// NewDmDelayWrapper returns a dm-delay wrapper stub.
func NewDmDelayWrapper(_ DockerClient) *DmDelayWrapper { return &DmDelayWrapper{} }

// InjectDmDelay always returns ErrDmDelayUnsupported — the mount swap that
// would route real I/O through the mapper device is not implemented.
func (dw *DmDelayWrapper) InjectDmDelay(_ context.Context, _ string, _ IODelayParams) error {
	return ErrDmDelayUnsupported
}

// RemoveDmDelay is a no-op; InjectDmDelay never installs anything to remove.
func (dw *DmDelayWrapper) RemoveDmDelay(_ context.Context, _ string) error { return nil }

// HasActiveMapping always returns false — no mappings are ever installed.
func (dw *DmDelayWrapper) HasActiveMapping(_ string) bool { return false }

// ErrDmDelayUnsupported is returned from any dm-delay entry point.
var ErrDmDelayUnsupported = fmt.Errorf(
	"disk_io method=\"dm-delay\" is not supported: the mapper device would be created but no code swaps the filesystem mount onto it, so no I/O is delayed; use method=\"dd\" instead",
)
