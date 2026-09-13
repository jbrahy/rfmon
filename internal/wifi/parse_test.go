package wifi

import (
	"encoding/binary"
	"testing"
)

// --- synthetic frame builders (no real captures) ---

// radiotapHeader builds gr-foo's fixed 17 byte radiotap header with the
// given SNR (signal) byte.
func radiotapHeader(signal byte) []byte {
	h := make([]byte, 17)
	binary.LittleEndian.PutUint16(h[0:2], 0)          // version
	binary.LittleEndian.PutUint16(h[2:4], 17)         // length
	binary.LittleEndian.PutUint32(h[4:8], 0x0000086e) // present
	h[8] = 0                                          // flags
	h[9] = 0                                          // rate
	binary.LittleEndian.PutUint32(h[10:14], 2437)     // channel (MHz, unused by parser)
	h[14] = signal                                    // signal / SNR
	h[15] = 0                                         // noise
	h[16] = 0                                         // antenna
	return h
}

// dot11Header builds a 24 byte 802.11 management frame header.
func dot11Header(fc0 byte, addr1, addr2, addr3 [6]byte) []byte {
	b := make([]byte, dot11HeaderLen)
	b[0] = fc0
	b[1] = 0
	copy(b[4:10], addr1[:])
	copy(b[10:16], addr2[:])
	copy(b[16:22], addr3[:])
	return b
}

// fixedParams builds the 12 byte beacon/probe-response fixed parameters
// with the given capability info.
func fixedParams(capInfo uint16) []byte {
	b := make([]byte, fixedParamsLen)
	binary.LittleEndian.PutUint16(b[8:10], 100) // beacon interval
	binary.LittleEndian.PutUint16(b[10:12], capInfo)
	return b
}

func elemSSIDBytes(ssid string) []byte {
	e := []byte{elemSSID, byte(len(ssid))}
	return append(e, []byte(ssid)...)
}

func elemDSChannelBytes(ch byte) []byte {
	return []byte{elemDSChannel, 1, ch}
}

// elemRSNBytes builds an RSN element with a single pairwise cipher (CCMP)
// and a single AKM suite.
func elemRSNBytes(akm [4]byte) []byte {
	var data []byte
	data = append(data, 1, 0)                   // version
	data = append(data, 0x00, 0x0F, 0xAC, 0x04) // group cipher: CCMP
	data = append(data, 1, 0)                   // pairwise cipher count
	data = append(data, 0x00, 0x0F, 0xAC, 0x04) // pairwise cipher: CCMP
	data = append(data, 1, 0)                   // AKM suite count
	data = append(data, akm[:]...)
	e := []byte{elemRSN, byte(len(data))}
	return append(e, data...)
}

var akmSAE = [4]byte{0x00, 0x0F, 0xAC, 0x08}
var akmPSK = [4]byte{0x00, 0x0F, 0xAC, 0x02}

func elemVendorWPABytes() []byte {
	data := []byte{0x00, 0x50, 0xF2, 0x01, 0x01, 0x00}
	e := []byte{elemVendorSpec, byte(len(data))}
	return append(e, data...)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

var bssid = [6]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}
var broadcast = [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
var randomizedClient = [6]byte{0x02, 0xaa, 0xbb, 0xcc, 0xdd, 0xee}

func beaconFrame(signal byte, capInfo uint16, elements ...[]byte) []byte {
	parts := []([]byte){
		radiotapHeader(signal),
		dot11Header(0x80, broadcast, bssid, bssid),
		fixedParams(capInfo),
	}
	parts = append(parts, elements...)
	return concat(parts...)
}

func probeResponseFrame(signal byte, capInfo uint16, elements ...[]byte) []byte {
	parts := []([]byte){
		radiotapHeader(signal),
		dot11Header(0x50, broadcast, bssid, bssid),
		fixedParams(capInfo),
	}
	parts = append(parts, elements...)
	return concat(parts...)
}

func probeRequestFrame(signal byte, transmitter [6]byte, elements ...[]byte) []byte {
	parts := []([]byte){
		radiotapHeader(signal),
		dot11Header(0x40, broadcast, transmitter, [6]byte{}),
	}
	parts = append(parts, elements...)
	return concat(parts...)
}

// --- tests ---

func TestParseBeaconWPA3(t *testing.T) {
	frame := beaconFrame(200, 0x0001,
		elemSSIDBytes("TestNet"),
		elemDSChannelBytes(6),
		elemRSNBytes(akmSAE),
	)
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if f.Kind != "ap" {
		t.Errorf("Kind = %q, want ap", f.Kind)
	}
	if f.FrameType != "beacon" {
		t.Errorf("FrameType = %q, want beacon", f.FrameType)
	}
	if f.SSID == nil || *f.SSID != "TestNet" {
		t.Errorf("SSID = %v, want TestNet", f.SSID)
	}
	if f.Hidden {
		t.Errorf("Hidden = true, want false")
	}
	if f.Channel != 6 {
		t.Errorf("Channel = %d, want 6", f.Channel)
	}
	if f.Security == nil || *f.Security != "wpa3" {
		t.Errorf("Security = %v, want wpa3", f.Security)
	}
}

func TestParseBeaconHiddenSSID(t *testing.T) {
	frame := beaconFrame(200, 0x0001,
		elemSSIDBytes(""),
		elemDSChannelBytes(6),
	)
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if !f.Hidden {
		t.Errorf("Hidden = false, want true")
	}
	if f.SSID != nil {
		t.Errorf("SSID = %v, want nil", f.SSID)
	}
}

func TestParseBeaconSecurityWPA2(t *testing.T) {
	frame := beaconFrame(200, 0x0001,
		elemSSIDBytes("Net"),
		elemRSNBytes(akmPSK),
	)
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if f.Security == nil || *f.Security != "wpa2" {
		t.Errorf("Security = %v, want wpa2", f.Security)
	}
}

func TestParseBeaconSecurityWPA(t *testing.T) {
	frame := beaconFrame(200, 0x0001,
		elemSSIDBytes("Net"),
		elemVendorWPABytes(),
	)
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if f.Security == nil || *f.Security != "wpa" {
		t.Errorf("Security = %v, want wpa", f.Security)
	}
}

func TestParseBeaconSecurityWEP(t *testing.T) {
	frame := beaconFrame(200, 0x0011, // privacy bit set
		elemSSIDBytes("Net"),
	)
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if f.Security == nil || *f.Security != "wep" {
		t.Errorf("Security = %v, want wep", f.Security)
	}
}

func TestParseBeaconSecurityOpen(t *testing.T) {
	frame := beaconFrame(200, 0x0001,
		elemSSIDBytes("Net"),
	)
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if f.Security == nil || *f.Security != "open" {
		t.Errorf("Security = %v, want open", f.Security)
	}
}

func TestParseProbeRequestRandomized(t *testing.T) {
	frame := probeRequestFrame(200, randomizedClient,
		elemSSIDBytes("Home"),
	)
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if f.Kind != "client" {
		t.Errorf("Kind = %q, want client", f.Kind)
	}
	if f.FrameType != "probe_request" {
		t.Errorf("FrameType = %q, want probe_request", f.FrameType)
	}
	if f.SSID == nil || *f.SSID != "Home" {
		t.Errorf("SSID = %v, want Home", f.SSID)
	}
	if !f.Randomized {
		t.Errorf("Randomized = false, want true")
	}
	if f.Security != nil {
		t.Errorf("Security = %v, want nil", f.Security)
	}
}

func TestParseProbeRequestWildcardSSID(t *testing.T) {
	frame := probeRequestFrame(200, randomizedClient,
		elemSSIDBytes(""),
	)
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if f.SSID != nil {
		t.Errorf("SSID = %v, want nil", f.SSID)
	}
}

func TestParseProbeResponse(t *testing.T) {
	frame := probeResponseFrame(200, 0x0001,
		elemSSIDBytes("Net"),
	)
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if f.Kind != "ap" {
		t.Errorf("Kind = %q, want ap", f.Kind)
	}
	if f.FrameType != "probe_response" {
		t.Errorf("FrameType = %q, want probe_response", f.FrameType)
	}
}

func TestParseDataFrameRejected(t *testing.T) {
	frame := concat(
		radiotapHeader(200),
		dot11Header(0x08, broadcast, bssid, bssid),
	)
	_, ok := Parse(frame)
	if ok {
		t.Errorf("Parse returned ok=true for a data frame, want false")
	}
}

func TestParseShortFrameRejected(t *testing.T) {
	frame := concat(radiotapHeader(200), []byte{0x80, 0x00, 0x00, 0x00})
	_, ok := Parse(frame)
	if ok {
		t.Errorf("Parse returned ok=true for a short frame, want false")
	}
}

func TestParseTruncatedElementRejected(t *testing.T) {
	frame := concat(
		radiotapHeader(200),
		dot11Header(0x80, broadcast, bssid, bssid),
		fixedParams(0x0001),
		[]byte{elemSSID, 200, 'a', 'b'}, // declares 200 bytes, only 2 follow
	)
	_, ok := Parse(frame)
	if ok {
		t.Errorf("Parse returned ok=true for a truncated element, want false")
	}
}

func TestParseSNR(t *testing.T) {
	frame := beaconFrame(42, 0x0001, elemSSIDBytes("Net"))
	f, ok := Parse(frame)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if f.SNR == nil || *f.SNR != 42 {
		t.Errorf("SNR = %v, want 42", f.SNR)
	}
}

func TestParseRadiotapUsesLengthField(t *testing.T) {
	// A radiotap header padded to 19 bytes (length field says 19, not the
	// usual 17): the 802.11 frame must start at byte 19, not byte 17.
	h := radiotapHeader(150)
	binary.LittleEndian.PutUint16(h[2:4], 19)
	h = append(h, 0x00, 0x00) // padding to reach the declared length
	dot11 := dot11Header(0x40, broadcast, randomizedClient, [6]byte{})
	frame := append(h, dot11...)

	snr, hasSNR, gotDot11, ok := ParseRadiotap(frame)
	if !ok {
		t.Fatalf("ParseRadiotap returned ok=false")
	}
	if !hasSNR || snr != 150 {
		t.Errorf("snr = %d, hasSNR = %v, want 150, true", snr, hasSNR)
	}
	if len(gotDot11) != len(dot11) {
		t.Fatalf("dot11 length = %d, want %d", len(gotDot11), len(dot11))
	}
	if gotDot11[0] != 0x40 {
		t.Errorf("dot11[0] = %#x, want 0x40 (length field not honored)", gotDot11[0])
	}
}

func TestParseRadiotapTruncatedHeaderRejected(t *testing.T) {
	_, _, _, ok := ParseRadiotap([]byte{0x00, 0x00, 0x11, 0x00})
	if ok {
		t.Errorf("ParseRadiotap returned ok=true for a truncated header, want false")
	}
}
