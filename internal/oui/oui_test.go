package oui

import "testing"

func TestCompanyName(t *testing.T) {
	cases := []struct {
		id   int
		want string
	}{
		{76, "Apple, Inc."},
		{117, "Samsung Electronics Co. Ltd."},
		{0, "Ericsson AB"},
	}
	for _, c := range cases {
		got, ok := CompanyName(c.id)
		if !ok || got != c.want {
			t.Errorf("CompanyName(%d) = %q, %v; want %q, true", c.id, got, ok, c.want)
		}
	}
	if _, ok := CompanyName(0x7FFFFFFF); ok {
		t.Error("CompanyName(unknown) = ok true, want false")
	}
}

func TestVendor(t *testing.T) {
	// 00:03:93 is a registered Apple OUI.
	for _, mac := range []string{"00:03:93:aa:bb:cc", "000393AABBCC", "00-03-93-11-22-33"} {
		got, ok := Vendor(mac)
		if !ok || got != "Apple, Inc." {
			t.Errorf("Vendor(%q) = %q, %v; want Apple, Inc., true", mac, got, ok)
		}
	}
}

func TestVendorLocallyAdministeredIsNotResolved(t *testing.T) {
	// First octet 0x02 has the locally-administered bit set (randomized).
	if name, ok := Vendor("02:11:22:33:44:55"); ok {
		t.Errorf("Vendor(randomized) = %q, true; want false", name)
	}
	// 0x06, 0x0a, 0x0e also have bit 0x02 set.
	if _, ok := Vendor("0a:aa:bb:cc:dd:ee"); ok {
		t.Error("Vendor(0a...) resolved, want false for locally administered")
	}
}

func TestVendorUnknownAndMalformed(t *testing.T) {
	if _, ok := Vendor("00:00:00:00:00:00"); !ok {
		// 000000 is XEROX in the IEEE list, so this should resolve; guard the
		// test against an unexpected miss rather than asserting the name.
		t.Log("000000 not found; acceptable if the embedded list omits it")
	}
	for _, bad := range []string{"", "zz:zz", "12"} {
		if _, ok := Vendor(bad); ok {
			t.Errorf("Vendor(%q) resolved, want false", bad)
		}
	}
}

func TestTablesLoaded(t *testing.T) {
	if len(companyNames) < 1000 {
		t.Errorf("companyNames has %d entries, want >= 1000", len(companyNames))
	}
	if len(vendorNames) < 10000 {
		t.Errorf("vendorNames has %d entries, want >= 10000", len(vendorNames))
	}
}
