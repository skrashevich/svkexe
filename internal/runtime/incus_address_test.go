package runtime

import (
	"testing"

	"github.com/lxc/incus/v6/shared/api"
)

func TestExtractAddressUsesPlatformNIC(t *testing.T) {
	bridge := api.InstanceStateNetwork{Hwaddr: "02:42:aa:bb:cc:dd", Addresses: []api.InstanceStateNetworkAddress{{Family: "inet", Scope: "global", Address: "172.17.0.1"}}}
	nic := api.InstanceStateNetwork{Hwaddr: "10:66:6a:5b:aa:30", Addresses: []api.InstanceStateNetworkAddress{{Family: "inet", Scope: "global", Address: "10.100.0.10"}}}
	for _, tc := range []struct {
		name    string
		state   *api.InstanceState
		ip, mac string
	}{
		{"Docker bridge only", &api.InstanceState{Network: map[string]api.InstanceStateNetwork{"docker0": bridge}}, "", ""},
		{"platform NIC with Docker", &api.InstanceState{Network: map[string]api.InstanceStateNetwork{"docker0": bridge, "eth0": nic}}, "10.100.0.10", nic.Hwaddr},
		{"no state", nil, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for range 100 {
				ip, mac := extractAddress(tc.state)
				if ip != tc.ip || mac != tc.mac {
					t.Fatalf("got (%q, %q), want (%q, %q)", ip, mac, tc.ip, tc.mac)
				}
			}
		})
	}
}
