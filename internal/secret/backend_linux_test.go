//go:build linux

package secret

import (
	"context"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestServiceUnknownFallsBack(t *testing.T) {
	kr := newFakeKeyring()
	kr.getErr = dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}
	s := tempStore(t, kr)
	k, err := s.EnsureAtRest(context.Background())
	if err != nil || k.Source != SourceFile {
		t.Fatalf("service unknown = %+v, %v", k, err)
	}
}
