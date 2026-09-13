// Package wifi parses radiotap-wrapped 802.11 management frames, as
// produced by gr-foo's wireshark_connector (pcap link type 127), into a
// summary of the AP or client that sent the frame.
package wifi

import (
	"encoding/binary"
	"fmt"
)

// radiotapMinHeaderLen is the smallest length gr-foo's radiotap header can
// declare while still including the fixed fields this package reads
// (through the antenna byte at offset 16).
const radiotapMinHeaderLen = 17

// signalPresentBit is bit 5 of the radiotap present field: antenna signal.
const signalPresentBit = 1 << 5

// dot11HeaderLen is the length of an 802.11 management frame header
// (frame control, duration, three addresses, sequence control).
const dot11HeaderLen = 24

// fixedParamsLen is the length of the beacon/probe-response fixed
// parameters (timestamp, beacon interval, capability info) that precede
// the tagged elements.
const fixedParamsLen = 12

// capabilityInfoOffset is the byte offset of the capability info field
// within a beacon or probe response frame.
const capabilityInfoOffset = 24 + 10 // MAC header + timestamp + beacon interval

const (
	elemSSID       = 0
	elemDSChannel  = 3
	elemRSN        = 48
	elemVendorSpec = 221
)

// privacyBit is bit 4 of the capability info field.
const privacyBit = 1 << 4

// Frame summarizes a beacon, probe response, or probe request captured
// over the air.
type Frame struct {
	Kind       string // "ap" or "client"
	MAC        string
	SSID       *string
	Hidden     bool
	Channel    int
	Security   *string // nil for clients
	Randomized bool
	SNR        *int
	FrameType  string // "beacon", "probe_response", "probe_request"
}

// ParseRadiotap reads gr-foo's fixed radiotap header from the front of b.
// It uses the length field (bytes 2-3, little-endian) to find the start of
// the 802.11 frame rather than assuming a fixed header size. snr is the
// signal byte (SNR in dB, 0..255) and hasSNR reports whether the present
// bitmask marks that field as populated. ok is false if b is too short to
// hold the declared header.
func ParseRadiotap(b []byte) (snr int, hasSNR bool, dot11 []byte, ok bool) {
	if len(b) < 8 {
		return 0, false, nil, false
	}
	length := int(binary.LittleEndian.Uint16(b[2:4]))
	if length < radiotapMinHeaderLen || len(b) < length {
		return 0, false, nil, false
	}
	present := binary.LittleEndian.Uint32(b[4:8])
	if present&signalPresentBit != 0 {
		snr = int(b[14])
		hasSNR = true
	}
	return snr, hasSNR, b[length:], true
}

// Parse parses a radiotap-wrapped 802.11 frame into a Frame summary. It
// returns ok=false for anything that is not a beacon, probe response, or
// probe request, or that is too short or truncated to parse safely.
func Parse(radiotapFrame []byte) (Frame, bool) {
	snr, hasSNR, dot11, ok := ParseRadiotap(radiotapFrame)
	if !ok || len(dot11) < dot11HeaderLen {
		return Frame{}, false
	}

	fc0 := dot11[0]
	typ := (fc0 >> 2) & 0x3
	subtype := (fc0 >> 4) & 0xF
	if typ != 0 {
		return Frame{}, false
	}

	var kind, frameType string
	switch subtype {
	case 8:
		kind, frameType = "ap", "beacon"
	case 5:
		kind, frameType = "ap", "probe_response"
	case 4:
		kind, frameType = "client", "probe_request"
	default:
		return Frame{}, false
	}

	var mac []byte
	var capInfo uint16
	elementsStart := dot11HeaderLen
	if kind == "ap" {
		if len(dot11) < dot11HeaderLen+fixedParamsLen {
			return Frame{}, false
		}
		mac = dot11[16:22] // addr3 (BSSID)
		capInfo = binary.LittleEndian.Uint16(dot11[capabilityInfoOffset : capabilityInfoOffset+2])
		elementsStart = dot11HeaderLen + fixedParamsLen
	} else {
		mac = dot11[10:16] // addr2 (transmitter)
	}

	var ssid *string
	hidden := false
	channel := 0
	hasRSN := false
	hasSAE := false
	hasVendorWPA := false

	pos := elementsStart
	for pos+2 <= len(dot11) {
		id := dot11[pos]
		elemLen := int(dot11[pos+1])
		dataStart := pos + 2
		dataEnd := dataStart + elemLen
		if dataEnd > len(dot11) {
			return Frame{}, false // element length runs past the buffer
		}
		data := dot11[dataStart:dataEnd]

		switch id {
		case elemSSID:
			if elemLen == 0 {
				if kind == "ap" {
					hidden = true
				}
			} else {
				s := string(data)
				ssid = &s
			}
		case elemDSChannel:
			if elemLen >= 1 {
				channel = int(data[0])
			}
		case elemRSN:
			hasRSN = true
			if rsnHasSAE(data) {
				hasSAE = true
			}
		case elemVendorSpec:
			if isVendorWPA(data) {
				hasVendorWPA = true
			}
		}
		pos = dataEnd
	}

	var security *string
	if kind == "ap" {
		s := classifySecurity(hasRSN, hasSAE, hasVendorWPA, capInfo)
		security = &s
	}

	return Frame{
		Kind:       kind,
		MAC:        formatMAC(mac),
		SSID:       ssid,
		Hidden:     hidden,
		Channel:    channel,
		Security:   security,
		Randomized: mac[0]&0x02 != 0,
		SNR:        snrPtr(snr, hasSNR),
		FrameType:  frameType,
	}, true
}

// classifySecurity implements the classification order from the design:
// RSN (SAE AKM -> wpa3, else wpa2), then vendor WPA IE, then the privacy
// capability bit, else open.
func classifySecurity(hasRSN, hasSAE, hasVendorWPA bool, capInfo uint16) string {
	switch {
	case hasRSN && hasSAE:
		return "wpa3"
	case hasRSN:
		return "wpa2"
	case hasVendorWPA:
		return "wpa"
	case capInfo&privacyBit != 0:
		return "wep"
	default:
		return "open"
	}
}

// rsnHasSAE reports whether an RSN element's AKM suite list includes the
// SAE suite 00-0F-AC-08. Every step is bounds-checked against data so a
// short or malformed RSN element yields false rather than a panic.
func rsnHasSAE(data []byte) bool {
	pos := 2 // skip version
	pos += 4 // skip group cipher suite
	if len(data) < pos+2 {
		return false
	}
	pairwiseCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
	pos += 2
	pos += pairwiseCount * 4
	if len(data) < pos+2 {
		return false
	}
	akmCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
	pos += 2
	for i := 0; i < akmCount; i++ {
		if len(data) < pos+4 {
			return false
		}
		suite := data[pos : pos+4]
		if suite[0] == 0x00 && suite[1] == 0x0F && suite[2] == 0xAC && suite[3] == 0x08 {
			return true
		}
		pos += 4
	}
	return false
}

// isVendorWPA reports whether a vendor-specific element (tag 221) is the
// Microsoft WPA IE: OUI 00-50-F2, type 1.
func isVendorWPA(data []byte) bool {
	return len(data) >= 4 &&
		data[0] == 0x00 && data[1] == 0x50 && data[2] == 0xF2 && data[3] == 0x01
}

// formatMAC renders a 6 byte hardware address as lowercase, colon-separated
// hex.
func formatMAC(mac []byte) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}

func snrPtr(snr int, hasSNR bool) *int {
	if !hasSNR {
		return nil
	}
	v := snr
	return &v
}
