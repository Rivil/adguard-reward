package adguard

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func pc(name string, ids ...string) PersistentClient {
	return PersistentClient{Name: name, IDs: ids}
}

func ips(ds []UnlabelledDevice) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.IP)
	}
	return out
}

func hostnames(ds []UnlabelledDevice) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.Hostname)
	}
	return out
}

func TestUnlabelled_ExcludesKnownIP(t *testing.T) {
	persistent := []PersistentClient{pc("Kid phone", "aa:bb:cc:dd:ee:01", "192.168.1.50")}
	auto := []AutoClient{{IP: "192.168.1.50", Hostname: "kid-phone", Source: "DHCP"}, {IP: "192.168.1.9", Hostname: "printer", Source: "ARP"}}
	if got := ips(Unlabelled(persistent, auto, nil)); !reflect.DeepEqual(got, []string{"192.168.1.9"}) {
		t.Errorf("unlabelled = %v, want only 192.168.1.9", got)
	}
}

func TestUnlabelled_IPv6Canonical(t *testing.T) {
	persistent := []PersistentClient{pc("Laptop", "2001:db8::0:1")}
	auto := []AutoClient{{IP: "2001:db8::1", Hostname: "laptop", Source: "rDNS"}, {IP: "2001:db8::2", Hostname: "other", Source: "rDNS"}}
	if got := ips(Unlabelled(persistent, auto, nil)); !reflect.DeepEqual(got, []string{"2001:db8::2"}) {
		t.Errorf("unlabelled = %v, want only 2001:db8::2", got)
	}
}

func TestUnlabelled_CIDR(t *testing.T) {
	persistent := []PersistentClient{pc("Lab", "192.168.1.0/28")}
	auto := []AutoClient{{IP: "192.168.1.9", Hostname: "printer", Source: "ARP"}, {IP: "192.168.1.16", Hostname: "outside", Source: "ARP"}}
	if got := ips(Unlabelled(persistent, auto, nil)); !reflect.DeepEqual(got, []string{"192.168.1.16"}) {
		t.Errorf("unlabelled = %v, want only 192.168.1.16 (outside the /28)", got)
	}
}

func TestUnlabelled_LeasedMAC(t *testing.T) {
	persistent := []PersistentClient{pc("Kid tablet", "aa:bb:cc:dd:ee:02")}
	auto := []AutoClient{{IP: "192.168.1.51", Hostname: "kid-tablet", Source: "DHCP"}, {IP: "192.168.1.52", Hostname: "other", Source: "DHCP"}}
	leases := []Lease{{MAC: "AA-BB-CC-DD-EE-02", IP: "192.168.1.51"}, {MAC: "aa:bb:cc:dd:ee:52", IP: "192.168.1.52"}}
	if got := ips(Unlabelled(persistent, auto, leases)); !reflect.DeepEqual(got, []string{"192.168.1.52"}) {
		t.Errorf("unlabelled = %v, want only 192.168.1.52", got)
	}
}

func TestUnlabelled_AllSources(t *testing.T) {
	auto := []AutoClient{
		{IP: "10.0.0.1", Hostname: "a", Source: "ARP"},
		{IP: "10.0.0.2", Hostname: "b", Source: "rDNS"},
		{IP: "10.0.0.3", Hostname: "c", Source: "DHCP"},
		{IP: "10.0.0.4", Hostname: "d", Source: "WHOIS"},
		{IP: "10.0.0.5", Hostname: "e", Source: "etc/hosts"},
	}
	got := Unlabelled(nil, auto, nil)
	if len(got) != 5 {
		t.Fatalf("unlabelled = %v, want all five sources", got)
	}
	for i, d := range got {
		if d.Source != auto[i].Source {
			t.Errorf("row %d source = %q, want %q", i, d.Source, auto[i].Source)
		}
	}
}

func TestUnlabelled_Order(t *testing.T) {
	a := []AutoClient{
		{IP: "192.168.1.40", Hostname: "", Source: "WHOIS"},
		{IP: "192.168.1.30", Hostname: "Zed-TV", Source: "rDNS"},
		{IP: "192.168.1.9", Hostname: "printer", Source: "ARP"},
		{IP: "192.168.1.20", Hostname: "alpha", Source: "DHCP"},
	}
	b := []AutoClient{a[2], a[0], a[3], a[1]}
	want := []string{"alpha", "printer", "Zed-TV", ""}
	for _, in := range [][]AutoClient{a, b} {
		if got := hostnames(Unlabelled(nil, in, nil)); !reflect.DeepEqual(got, want) {
			t.Errorf("order = %q, want %q", got, want)
		}
	}

	ties := []AutoClient{{IP: "10.0.0.10", Hostname: "tv"}, {IP: "10.0.0.9", Hostname: "TV"}, {IP: "10.0.0.2"}, {IP: "10.0.0.1"}}
	if got := ips(Unlabelled(nil, ties, nil)); !reflect.DeepEqual(got, []string{"10.0.0.9", "10.0.0.10", "10.0.0.1", "10.0.0.2"}) {
		t.Errorf("tie-break = %v, want numeric IP order within a hostname and among blanks", got)
	}
}

func TestUnlabelled_SuggestedIDs(t *testing.T) {
	auto := []AutoClient{{IP: "192.168.1.9", Hostname: "printer"}, {IP: "192.168.1.30", Hostname: "tv"}}
	leases := []Lease{{MAC: "AA:BB:CC:DD:EE:09", IP: "192.168.1.9"}}
	got := Unlabelled(nil, auto, leases)
	if !reflect.DeepEqual(got[0].SuggestedIDs, []string{"192.168.1.9", "aa:bb:cc:dd:ee:09"}) {
		t.Errorf("leased printer suggested = %v, want [ip mac]", got[0].SuggestedIDs)
	}
	if !reflect.DeepEqual(got[1].SuggestedIDs, []string{"192.168.1.30"}) {
		t.Errorf("unleased tv suggested = %v, want [ip]", got[1].SuggestedIDs)
	}
	if empty := Unlabelled(nil, nil, nil); empty == nil {
		t.Error("Unlabelled(nil...) must be [] not nil")
	}
}

func TestUnlabelled_Fixture(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	res, err := c.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	leases, err := c.Leases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := Unlabelled(res.Persistent, res.AutoClients, leases)
	if want := []string{"alpha", "printer", "Zed-TV", ""}; !reflect.DeepEqual(hostnames(got), want) {
		t.Errorf("fixture unlabelled = %q, want %q (192.168.1.50 and the Kid tablet lease excluded)", hostnames(got), want)
	}
}

func TestLeases_Static(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	got, err := c.Leases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Lease{
		{MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.1.9", Hostname: "printer"},
		{MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.1.51", Hostname: "kid-tablet"},
		{MAC: "aa:bb:cc:dd:ee:20", IP: "192.168.1.20", Hostname: "alpha"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Leases = %+v, want dynamic then static %+v", got, want)
	}

	s.SetResponse("/control/dhcp/status", 200, []byte(`{"leases":null,"static_leases":null}`))
	got, err = c.Leases(context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("null leases = %#v, %v; want [] nil", got, err)
	}
}

func TestLeases_Error(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.SetStatus("/control/dhcp/status", 500)
	_, err := c.Leases(context.Background())
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 500 {
		t.Errorf("err = %v, want *StatusError 500", err)
	}
}
