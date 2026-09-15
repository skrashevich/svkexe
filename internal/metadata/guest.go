package metadata

// The guest half of the metadata service: what has to exist inside a VM for
// http://169.254.169.254/ to answer there.
//
// In the ordinary case nothing does. A VM's routing table has a default route to
// the host bridge and no entry for 169.254.0.0/16 — the kernel does not
// special-case link-local IPv4 on output — so a packet addressed to the metadata
// service already leaves through that default route, and the host redirects it on
// the bridge. The pieces below exist for the case that breaks it: a guest that
// carries a zeroconf "169.254.0.0/16 dev eth0 scope link" route — NetworkManager
// adds one, and so do some images — would try to find the service on the local
// segment, where nothing answers, because the host holds no such address. A /32
// is more specific than that /16 and wins, which turns "usually works" into
// "works".
//
// Nothing here is load-bearing for a VM that never had the /16, which is why a
// failure to install it is logged rather than allowed to fail a VM's setup.
const (
	// GuestRouteScriptPath is the helper the unit runs. It lives under
	// /usr/local/sbin because it is the platform's, not the distribution's.
	GuestRouteScriptPath = "/usr/local/sbin/svkexe-metadata-route"
	// GuestRouteUnitName is the systemd unit that runs it at boot, so a VM
	// restarted from inside keeps the route without the gateway touching it.
	GuestRouteUnitName = "svkexe-metadata-route.service"
	// GuestRouteUnitPath is where that unit is installed.
	GuestRouteUnitPath = "/etc/systemd/system/" + GuestRouteUnitName
)

// GuestRouteScript returns the helper that installs the route.
//
// It is written to be safe to run at any moment of a boot: `ip route replace`
// creates or overwrites, never duplicates or fails on an existing entry. A VM
// whose network has not come up yet gets no route at all and says so, because
// the only route that works here goes via a default gateway that does not exist
// yet; the unit's Restart= is what turns that into "not yet" rather than "never".
func GuestRouteScript() string {
	return `#!/bin/sh
# Installed by the svkexe gateway — edits will be overwritten.
#
# Points this VM at the instance metadata service on ` + Address + `.
# A default route already carries that address to the host; this pins it so that
# a zeroconf 169.254.0.0/16 route, which would send it to the local segment
# instead, cannot take it away.
#
# The route must go via the default gateway, never on-link: the host does not
# hold ` + Address + ` as an address of its own and so answers no ARP for it.
# It redirects the traffic on the bridge instead, which only sees packets that
# were routed to it.
set -eu

# 'default via GW dev DEV …' is the form DHCP produces. Anything else — an
# on-link default, a multipath route — is not something to guess at: the fields
# move, and a wrong guess installs a route to nowhere that looks installed.
route=$(ip -4 route show default 2>/dev/null | head -n 1)
gw=""
dev=""
set -- ${route}
while [ "$#" -gt 0 ]; do
	case "$1" in
		via) gw="${2:-}"; shift 2 ;;
		dev) dev="${2:-}"; shift 2 ;;
		*) shift ;;
	esac
done

if [ -z "${gw}" ] || [ -z "${dev}" ]; then
	echo "no usable IPv4 default route yet: ${route:-none}" >&2
	# Non-zero so the unit's restart tries again. Until it succeeds the address
	# is simply carried by whatever default route does appear, which is the
	# ordinary case anyway.
	exit 1
fi

ip route replace ` + Address + `/32 via "${gw}" dev "${dev}"
`
}

// GuestRouteUnit returns the systemd unit that runs the helper.
//
// It is a oneshot rather than a service to keep alive: the route outlives the
// process that installed it. RemainAfterExit is what makes `systemctl enable
// --now` idempotent across repeated gateway setups, and the restart is there for
// the failure that matters — a boot where DHCP had not produced a default route
// yet, which the helper reports as a failure precisely so this can retry it.
//
// StartLimitIntervalSec=0 turns off systemd's start rate limiting for this unit.
// With it on, five retries five seconds apart put the unit in a permanent failed
// state on a VM whose network takes half a minute — the exact VM this is for.
//
// Known limit: systemd-networkd removes routes it did not configure when it
// reconfigures a link (ManageForeignRoutes defaults to yes), and a oneshot that
// already succeeded will not re-run. Losing the route costs nothing on a guest
// whose default route still carries the address, which is every guest the
// platform ships; it only matters on one that also carries a zeroconf /16, and
// there the next gateway setup reinstates it.
func GuestRouteUnit() string {
	return `[Unit]
Description=svkexe instance metadata route
Documentation=https://github.com/skrashevich/svkexe
After=network.target
Wants=network.target
StartLimitIntervalSec=0

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=` + GuestRouteScriptPath + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`
}
