package drivers

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"
)

func writeRdmaAssociation(t *testing.T, root, device, netdev string) {
	t.Helper()
	dir := filepath.Join(root, device, "ports/1/gid_attrs/ndevs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0"), []byte(netdev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRdmaSelectionUsesNetdevNotDeviceName(t *testing.T) {
	root := t.TempDir()
	link := &netlink.Device{LinkAttrs: netlink.LinkAttrs{Name: "eth1"}}
	tail := &netlink.RdmaLink{Attrs: netlink.RdmaLinkAttrs{Name: "mlx5_0"}}
	front := &netlink.RdmaLink{Attrs: netlink.RdmaLinkAttrs{Name: "mlx5_1", NodeGuid: "not-an-erdma-guid"}}
	writeRdmaAssociation(t, root, tail.Attrs.Name, "bond0")
	writeRdmaAssociation(t, root, front.Attrs.Name, "eth1")
	got, err := findERdmaLink(link, []*netlink.RdmaLink{tail, front}, root)
	if err != nil || got != front {
		t.Fatalf("expected ENI-associated mlx5 device, got %v, %v", got, err)
	}
	if _, err := findERdmaLink(link, []*netlink.RdmaLink{tail}, root); !errors.Is(err, ErrERdmaLinkNotFound) {
		t.Fatalf("tail device must not match: %v", err)
	}
	other := &netlink.RdmaLink{Attrs: netlink.RdmaLinkAttrs{Name: "renamed_rdma"}}
	writeRdmaAssociation(t, root, other.Attrs.Name, "eth1")
	if _, err := findERdmaLink(link, []*netlink.RdmaLink{front, other}, root); err == nil {
		t.Fatal("ambiguous association must not expose an arbitrary device")
	}
}

func TestRdmaGUIDFallbackOnlyForLegacyErdmaDriver(t *testing.T) {
	mac, err := net.ParseMAC("00:16:3e:00:00:01")
	if err != nil {
		t.Fatal(err)
	}
	link := &netlink.Device{LinkAttrs: netlink.LinkAttrs{Name: "eth1", HardwareAddr: mac}}
	for _, tc := range []struct {
		name   string
		module string
		mapped bool
		want   bool
	}{
		{name: "legacy erdma", module: "erdma", want: true},
		{name: "foreign GUID", module: "mlx5_core"},
		{name: "association overrides GUID", module: "erdma", mapped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			device := &netlink.RdmaLink{Attrs: netlink.RdmaLinkAttrs{Name: "renamed_rdma", NodeGuid: "01:00:00:fe:ff:3e:16:02"}}
			dir := filepath.Join(root, device.Attrs.Name, "device/driver")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/sys/module/"+tc.module, filepath.Join(dir, "module")); err != nil {
				t.Fatal(err)
			}
			if tc.mapped {
				writeRdmaAssociation(t, root, device.Attrs.Name, "bond0")
			}
			got, err := findERdmaLink(link, []*netlink.RdmaLink{device}, root)
			if tc.want {
				if err != nil || got != device {
					t.Fatalf("legacy eRDMA GUID must still work: %v, %v", got, err)
				}
			} else if !errors.Is(err, ErrERdmaLinkNotFound) {
				t.Fatalf("must not infer non-ENI association from GUID: %v", err)
			}
		})
	}
}
