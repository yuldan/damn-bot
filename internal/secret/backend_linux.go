//go:build linux

package secret

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/godbus/dbus/v5"
)

// backendPresent avoids godbus autolaunch (dbus-launch + os.Setenv) on
// headless hosts: only use an already running session bus.
func backendPresent() bool {
	if addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); addr != "" && addr != "autolaunch:" {
		return true
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "bus")); err == nil { //nolint:gosec // runtime dir
			return true
		}
	}
	_, err := os.Stat(fmt.Sprintf("/run/user/%d/bus", os.Getuid()))
	return err == nil
}

// backendAbsent recognizes "no Secret Service on an existing bus" and a stale
// socket. During provisioning only, these permit the file fallback; after the
// marker exists the recorded source stays strict.
func backendAbsent(err error) bool {
	if de, ok := errors.AsType[dbus.Error](err); ok {
		switch de.Name {
		case "org.freedesktop.DBus.Error.ServiceUnknown",
			"org.freedesktop.DBus.Error.NameHasNoOwner",
			"org.freedesktop.DBus.Error.FileNotFound",
			"org.freedesktop.DBus.Error.Disconnected":
			return true
		}
	}
	var op *net.OpError
	return errors.As(err, &op)
}
