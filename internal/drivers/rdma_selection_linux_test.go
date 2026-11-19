package drivers

import (
	"errors"
	"net"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestERdmaSelectionExcludesTailDevices(t *testing.T) {
	mac, err := net.ParseMAC("00:16:3e:00:00:01")
	if err != nil {
		t.Fatal(err)
	}
	link := &netlink.Device{LinkAttrs: netlink.LinkAttrs{Name: "eth1", HardwareAddr: mac}}
	// Even a foreign device with a matching GUID must never be selected.
	guid := "01:00:00:fe:ff:3e:16:02"
	foreign := []*netlink.RdmaLink{
		{Attrs: netlink.RdmaLinkAttrs{Name: "mlx5_0", NodeGuid: guid}},
		{Attrs: netlink.RdmaLinkAttrs{Name: "troce_bond_0", NodeGuid: guid}},
		{Attrs: netlink.RdmaLinkAttrs{Name: "troce_bond_1", NodeGuid: "not-an-erdma-guid"}},
	}
	if _, err := findERdmaLink(link, foreign); !errors.Is(err, ErrERdmaLinkNotFound) {
		t.Fatalf("foreign devices must not match: %v", err)
	}
	erdma := &netlink.RdmaLink{Attrs: netlink.RdmaLinkAttrs{Name: "erdma_1", NodeGuid: guid}}
	got, err := findERdmaLink(link, append(foreign, erdma))
	if err != nil || got != erdma {
		t.Fatalf("expected only the matching eRDMA device, got %v, %v", got, err)
	}
	for _, device := range foreign {
		if _, err := GetERdmaDevPathsFromRdmaLink(device); err == nil {
			t.Fatalf("must refuse device paths for %s", device.Attrs.Name)
		}
	}
}
