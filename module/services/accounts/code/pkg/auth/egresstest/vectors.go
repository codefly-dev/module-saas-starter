// Package egresstest is the set of addresses every egress guard in this host
// is held to (the paths with a guard are listed in auth/public_address.go). Each guard that decides whether a connection may leave for a
// tenant-named destination answers through auth.IsPublicDestination, and each
// guard's own test runs these vectors against it, so a guard that stops using
// that classifier — or a classifier change that re-admits a range — fails a
// test named after the range.
package egresstest

import "net"

// Vector is one address, named after the range it belongs to.
type Vector struct {
	Name string
	Host string
}

// DialAddress is the vector as a dialer's Control hook receives it. Every guard
// judges the address alone, so the port is arbitrary.
func (v Vector) DialAddress() string { return net.JoinHostPort(v.Host, "443") }

// RawDialAddress is a dial input in the exact form given, for the shapes a
// guard must refuse because it cannot classify them at all.
type RawDialAddress struct {
	Name    string
	Address string
}

// Refused is one named case per range an egress guard must refuse: every
// entry of the IANA IPv4 and IPv6 special-purpose registries, the IPv6 space
// outside the 2000::/3 global unicast allocation, and the v4-mapped and zoned
// forms. A case is named after its range, so a change that
// re-admits one range fails a test that says which.
var Refused = []Vector{
	// Ranges Go's net.IP predicates call global unicast.
	{"shared address space 100.64.0.0/10, low end", "100.64.0.1"},
	{"shared address space 100.64.0.0/10, cloud metadata 100.100.100.200", "100.100.100.200"},
	{"shared address space 100.64.0.0/10, high end", "100.127.255.255"},
	{"NAT64 64:ff9b::/96 wrapping the link-local metadata address", "64:ff9b::a9fe:a9fe"},
	{"NAT64 64:ff9b::/96 wrapping private 10.0.0.1", "64:ff9b::a00:1"},
	{"NAT64 64:ff9b::/96 wrapping a public address", "64:ff9b::808:808"},
	{"local-use NAT64 64:ff9b:1::/48", "64:ff9b:1::a9fe:a9fe"},
	{"6to4 2002::/16 wrapping the link-local metadata address", "2002:a9fe:a9fe::1"},
	{"6to4 2002::/16 wrapping a public address", "2002:808:808::1"},
	{"Teredo 2001::/32", "2001:0:4136:e378:8000:63bf:3fff:fdd2"},
	{"benchmarking 198.18.0.0/15, low end", "198.18.0.1"},
	{"benchmarking 198.18.0.0/15, high end", "198.19.255.255"},
	{"IETF protocol assignments 192.0.0.0/24", "192.0.0.170"},
	{"reserved 240.0.0.0/4, low end", "240.0.0.1"},
	{"reserved 240.0.0.0/4, high end", "255.255.255.254"},

	// Loopback, private, link-local, unspecified and link-local multicast.
	{"loopback 127.0.0.0/8", "127.0.0.1"},
	{"loopback 127.0.0.0/8, high end", "127.255.255.254"},
	{"loopback ::1", "::1"},
	{"private 10.0.0.0/8", "10.1.2.3"},
	{"private 172.16.0.0/12, low end", "172.16.0.1"},
	{"private 172.16.0.0/12, high end", "172.31.255.255"},
	{"private 192.168.0.0/16", "192.168.0.1"},
	{"link-local 169.254.0.0/16, cloud metadata", "169.254.169.254"},
	{"link-local fe80::/10", "fe80::1"},
	{"unspecified 0.0.0.0", "0.0.0.0"},
	{"unspecified ::", "::"},
	{"link-local multicast 224.0.0.0/24", "224.0.0.1"},
	{"link-local multicast ff02::/16", "ff02::1"},

	// The rest of the IPv4 special-purpose registry.
	{"this network 0.0.0.0/8", "0.1.2.3"},
	{"multicast 224.0.0.0/4", "239.1.1.1"},
	{"limited broadcast 255.255.255.255", "255.255.255.255"},
	{"documentation 192.0.2.0/24", "192.0.2.1"},
	{"documentation 198.51.100.0/24", "198.51.100.1"},
	{"documentation 203.0.113.0/24", "203.0.113.1"},
	{"AS112 192.31.196.0/24", "192.31.196.1"},
	{"AMT 192.52.193.0/24", "192.52.193.1"},
	{"6to4 relay anycast 192.88.99.0/24", "192.88.99.1"},
	{"AS112 direct delegation 192.175.48.0/24", "192.175.48.1"},

	// The rest of the IPv6 special-purpose registry.
	{"discard-only 100::/64", "100::1"},
	{"dummy prefix 100:0:0:1::/64", "100:0:0:1::1"},
	{"IETF protocol assignments 2001::/23, unassigned remainder", "2001:100::1"},
	{"IETF protocol assignments 2001::/23, port control anycast", "2001:1::1"},
	{"IETF protocol assignments 2001::/23, benchmarking", "2001:2::1"},
	{"IETF protocol assignments 2001::/23, ORCHIDv2", "2001:20::1"},
	{"documentation 2001:db8::/32", "2001:db8::1"},
	{"documentation 3fff::/20", "3fff::1"},
	{"segment routing 5f00::/16", "5f00::1"},
	{"unique local fc00::/7", "fc00::1"},
	{"unique local fc00::/7, fd00::/8 half", "fd00::1"},
	{"multicast ff00::/8", "ff0e::1"},
	{"interface-local multicast ff01::/16", "ff01::1"},

	// IPv6 outside the 2000::/3 global unicast allocation, which Go also calls
	// global unicast and the special-purpose registry does not list.
	{"IPv4-compatible ::/96 wrapping the link-local metadata address", "::a9fe:a9fe"},
	{"IPv4-compatible ::/96 wrapping private 10.0.0.1", "::a00:1"},
	{"site-local fec0::/10", "fec0::1"},
	{"reserved by IETF 4000::/3", "4001::1"},
	{"reserved by IETF 8000::/1", "8000::1"},

	// A v4-mapped address is judged as the IPv4 address it wraps.
	{"v4-mapped loopback", "::ffff:127.0.0.1"},
	{"v4-mapped private", "::ffff:10.0.0.1"},
	{"v4-mapped link-local metadata", "::ffff:169.254.169.254"},
	{"v4-mapped shared address space", "::ffff:100.100.100.200"},
	{"v4-mapped benchmarking", "::ffff:198.18.0.1"},

	// A zone scopes an address to one interface; nothing public carries one.
	{"zoned link-local", "fe80::1%eth0"},
	{"zoned unique local", "fd00::1%lo0"},
	{"zoned global unicast", "2606:4700::1111%eth0"},
	// Unmap drops the zone, so only the zone refusal decides this one.
	{"zoned v4-mapped public", "::ffff:8.8.8.8%eth0"},
}

// Unclassifiable are dial inputs a guard cannot classify: it fails closed.
var Unclassifiable = []RawDialAddress{
	{"unparsable host", "not-an-ip:80"},
	{"hostname instead of an address", "example.com:443"},
	{"empty", ""},
	{"no port", "1.1.1.1"},
	{"bracketed address without a port", "[2606:4700::1111]"},
	{"IPv4 with a leading zero", "01.1.1.1:80"},
}

// Admitted are public addresses, several sitting one step outside a refused
// range, so a refused range that widens fails a named test too.
var Admitted = []Vector{
	{"public IPv4", "8.8.8.8"},
	{"public IPv4, second", "1.1.1.1"},
	{"just below shared address space", "100.63.255.255"},
	{"just above shared address space", "100.128.0.0"},
	{"just below benchmarking", "198.17.255.255"},
	{"just above benchmarking", "198.20.0.0"},
	{"just above IETF protocol assignments", "192.0.1.1"},
	{"just below multicast", "223.255.255.254"},
	{"v4-mapped public", "::ffff:8.8.8.8"},
	{"public IPv6", "2606:4700:4700::1111"},
	{"public IPv6 just above the 2001::/23 assignments", "2001:200::1"},
	{"public IPv6 outside 2001::/23", "2001:4860:4860::8888"},
	{"public IPv6 just above 6to4", "2003::1"},
	{"public IPv6, RIPE allocation", "2a00:1450:4001::1"},
}
