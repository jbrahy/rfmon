// Package oui resolves Bluetooth SIG company identifiers and IEEE MAC OUIs to
// human-readable organization names. The lookup tables are the official public
// lists, embedded gzipped and parsed once at startup.
//
//   - company_identifiers.tsv.gz: Bluetooth SIG assigned company identifiers
//     (https://www.bluetooth.com/specifications/assigned-numbers/), one
//     "<decimal id>\t<name>" line per company.
//   - oui.tsv.gz: IEEE MA-L assignments (https://standards-oui.ieee.org/),
//     one "<6 hex OUI>\t<organization>" line per assignment.
package oui

import (
	"bufio"
	"compress/gzip"
	_ "embed"
	"strconv"
	"strings"
)

//go:embed company_identifiers.tsv.gz
var companyGz []byte

//go:embed oui.tsv.gz
var ouiGz []byte

var (
	companyNames map[int]string
	vendorNames  map[string]string
)

func init() {
	companyNames = loadInt(companyGz)
	vendorNames = loadStr(ouiGz)
}

func loadInt(gz []byte) map[int]string {
	m := map[int]string{}
	forEachLine(gz, func(key, val string) {
		if id, err := strconv.Atoi(key); err == nil {
			m[id] = val
		}
	})
	return m
}

func loadStr(gz []byte) map[string]string {
	m := map[string]string{}
	forEachLine(gz, func(key, val string) {
		m[key] = val
	})
	return m
}

func forEachLine(gz []byte, fn func(key, val string)) {
	zr, err := gzip.NewReader(strings.NewReader(string(gz)))
	if err != nil {
		return
	}
	defer zr.Close()
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 1<<16), 1<<16)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "\t")
		if ok {
			fn(key, val)
		}
	}
}

// CompanyName returns the Bluetooth SIG company name for a manufacturer
// company identifier, and false if the id is unknown.
func CompanyName(id int) (string, bool) {
	name, ok := companyNames[id]
	return name, ok
}

// Vendor returns the IEEE-registered organization for a MAC address, matching
// on the first three octets (the OUI). The MAC may use any common separator or
// none; case is ignored. It returns false when the address is locally
// administered (the second-least-significant bit of the first octet is set,
// which includes randomized addresses) or when the OUI is not registered.
func Vendor(mac string) (string, bool) {
	hexOnly := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
			return r
		default:
			return -1
		}
	}, mac)
	if len(hexOnly) < 6 {
		return "", false
	}
	first, err := strconv.ParseUint(hexOnly[0:2], 16, 8)
	if err != nil {
		return "", false
	}
	if first&0x02 != 0 {
		// Locally administered (includes randomized addresses); the OUI is
		// not a real manufacturer assignment.
		return "", false
	}
	name, ok := vendorNames[strings.ToUpper(hexOnly[0:6])]
	return name, ok
}
