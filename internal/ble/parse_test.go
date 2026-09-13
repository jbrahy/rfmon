package ble

import (
	"encoding/binary"
	"testing"
)

// --- synthetic packet builders (no real captures) ---

// lePhdr builds ice9-bluetooth's 10 byte LE pseudo-header with the given
// signal power (dBm). The other fields (noise power, aa_offenses, ref_aa,
// flags) are unused by the parser and left zero.
func lePhdr(rfChannel byte, signalPower int8) []byte {
	b := make([]byte, 10)
	b[0] = rfChannel
	b[1] = byte(signalPower)
	b[2] = 0                                  // noise_power
	b[3] = 0                                  // aa_offenses
	binary.LittleEndian.PutUint32(b[4:8], 0)  // ref_aa
	binary.LittleEndian.PutUint16(b[8:10], 0) // flags
	return b
}

// accessAddressBytes encodes a 4 byte access address, little-endian.
func accessAddressBytes(aa uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, aa)
	return b
}

// pduHeaderBytes builds the 2 byte PDU header + length prefix.
func pduHeaderBytes(pduType byte, txAdd bool, payloadLen int) []byte {
	h := pduType & 0x0F
	if txAdd {
		h |= 0x40
	}
	return []byte{h, byte(payloadLen)}
}

// advABytes returns the on-air AdvA field (little-endian) for a MAC given
// in normal display order (most significant octet first).
func advABytes(display [6]byte) []byte {
	b := make([]byte, 6)
	for i := 0; i < 6; i++ {
		b[i] = display[5-i]
	}
	return b
}

// ad builds one AD structure: a length byte (type + value), the type
// byte, then the value.
func ad(adType byte, value ...byte) []byte {
	out := []byte{byte(1 + len(value)), adType}
	return append(out, value...)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// blePacket assembles a full LE pseudo-header packet: pseudo-header +
// access address + PDU header/length + payload.
func blePacket(signalPower int8, aa uint32, pduType byte, txAdd bool, payload []byte) []byte {
	return concat(
		lePhdr(37, signalPower),
		accessAddressBytes(aa),
		pduHeaderBytes(pduType, txAdd, len(payload)),
		payload,
	)
}

const advAA = 0x8E89BED6

var sensorMAC = [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

// --- tests ---

func TestParseADVIndPublicWithNameAndManufacturer(t *testing.T) {
	manufValue := []byte{0x4C, 0x00, 0x01, 0x02} // company id 0x004C LE + 2 bytes
	payload := concat(
		advABytes(sensorMAC),
		ad(0x01, 0x06),                // flags
		ad(0x09, []byte("Sensor")...), // complete local name
		ad(0xFF, manufValue...),       // manufacturer specific data
	)
	pkt := blePacket(-60, advAA, 0, false, payload)

	adv, ok := Parse(pkt)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if adv.Address != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("Address = %q, want aa:bb:cc:dd:ee:ff", adv.Address)
	}
	if adv.AddressType != "public" {
		t.Errorf("AddressType = %q, want public", adv.AddressType)
	}
	if adv.PDUType != "ADV_IND" {
		t.Errorf("PDUType = %q, want ADV_IND", adv.PDUType)
	}
	if adv.Name == nil || *adv.Name != "Sensor" {
		t.Errorf("Name = %v, want Sensor", adv.Name)
	}
	if adv.CompanyID == nil || *adv.CompanyID != 0x4C {
		t.Errorf("CompanyID = %v, want 0x4C", adv.CompanyID)
	}
	if adv.ManufacturerHex == nil || *adv.ManufacturerHex != "4c000102" {
		t.Errorf("ManufacturerHex = %v, want 4c000102", adv.ManufacturerHex)
	}
	if adv.RSSI == nil || *adv.RSSI != -60 {
		t.Errorf("RSSI = %v, want -60", adv.RSSI)
	}
}

func TestParseAddressTypeRandomStatic(t *testing.T) {
	mac := [6]byte{0xc1, 0x02, 0x03, 0x04, 0x05, 0x06} // top two bits of 0xc1 = 11
	payload := advABytes(mac)
	pkt := blePacket(-50, advAA, 0, true, payload)

	adv, ok := Parse(pkt)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if adv.AddressType != "random_static" {
		t.Errorf("AddressType = %q, want random_static", adv.AddressType)
	}
}

func TestParseAddressTypeRandomResolvable(t *testing.T) {
	mac := [6]byte{0x41, 0x02, 0x03, 0x04, 0x05, 0x06} // top two bits of 0x41 = 01
	payload := advABytes(mac)
	pkt := blePacket(-50, advAA, 0, true, payload)

	adv, ok := Parse(pkt)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if adv.AddressType != "random_resolvable" {
		t.Errorf("AddressType = %q, want random_resolvable", adv.AddressType)
	}
}

func TestParseAddressTypeRandomNonresolvable(t *testing.T) {
	mac := [6]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06} // top two bits of 0x01 = 00
	payload := advABytes(mac)
	pkt := blePacket(-50, advAA, 0, true, payload)

	adv, ok := Parse(pkt)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if adv.AddressType != "random_nonresolvable" {
		t.Errorf("AddressType = %q, want random_nonresolvable", adv.AddressType)
	}
}

func TestParseScanRspServiceUUIDs(t *testing.T) {
	uuids := make([]byte, 4)
	binary.LittleEndian.PutUint16(uuids[0:2], 0x1809)
	binary.LittleEndian.PutUint16(uuids[2:4], 0x180d)
	payload := concat(
		advABytes(sensorMAC),
		ad(0x03, uuids...), // complete list of 16-bit service UUIDs
	)
	pkt := blePacket(-55, advAA, 4, false, payload)

	adv, ok := Parse(pkt)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if adv.PDUType != "SCAN_RSP" {
		t.Errorf("PDUType = %q, want SCAN_RSP", adv.PDUType)
	}
	want := []string{"1809", "180d"}
	if len(adv.ServiceUUIDs) != len(want) {
		t.Fatalf("ServiceUUIDs = %v, want %v", adv.ServiceUUIDs, want)
	}
	for i, u := range want {
		if adv.ServiceUUIDs[i] != u {
			t.Errorf("ServiceUUIDs[%d] = %q, want %q", i, adv.ServiceUUIDs[i], u)
		}
	}
}

func TestParseTxPower(t *testing.T) {
	txPowerLevel := int8(-70)
	payload := concat(
		advABytes(sensorMAC),
		ad(0x0A, byte(txPowerLevel)), // TX power level
	)
	pkt := blePacket(-50, advAA, 6, false, payload) // ADV_SCAN_IND

	adv, ok := Parse(pkt)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if adv.PDUType != "ADV_SCAN_IND" {
		t.Errorf("PDUType = %q, want ADV_SCAN_IND", adv.PDUType)
	}
	if adv.TxPower == nil || *adv.TxPower != -70 {
		t.Errorf("TxPower = %v, want -70", adv.TxPower)
	}
}

func TestParseNonAdvertisingAccessAddress(t *testing.T) {
	payload := advABytes(sensorMAC)
	pkt := blePacket(-50, 0x11223344, 0, false, payload)

	_, ok := Parse(pkt)
	if ok {
		t.Errorf("Parse returned ok=true, want false for non-advertising access address")
	}
}

func TestParseScanReqRejected(t *testing.T) {
	// SCAN_REQ payload is ScanA (6) + AdvA (6), but the PDU type alone is
	// enough to reject it.
	payload := concat(advABytes(sensorMAC), advABytes(sensorMAC))
	pkt := blePacket(-50, advAA, 3, false, payload)

	_, ok := Parse(pkt)
	if ok {
		t.Errorf("Parse returned ok=true, want false for SCAN_REQ")
	}
}

func TestParseADVExtIndRejected(t *testing.T) {
	payload := advABytes(sensorMAC)
	pkt := blePacket(-50, advAA, 7, false, payload)

	_, ok := Parse(pkt)
	if ok {
		t.Errorf("Parse returned ok=true, want false for ADV_EXT_IND")
	}
}

func TestParseTruncatedADLength(t *testing.T) {
	payload := concat(
		advABytes(sensorMAC),
		ad(0x09, []byte("Sensor")...), // valid complete local name
		[]byte{0x05, 0xFF},            // AD claims 5 bytes but none follow
	)
	pkt := blePacket(-50, advAA, 0, false, payload)

	adv, ok := Parse(pkt)
	if !ok {
		t.Fatalf("Parse returned ok=false, want true (AdvA was present)")
	}
	if adv.Name == nil || *adv.Name != "Sensor" {
		t.Errorf("Name = %v, want Sensor (parsed before the truncated AD)", adv.Name)
	}
	if adv.ManufacturerHex != nil {
		t.Errorf("ManufacturerHex = %v, want nil (truncated AD never parsed)", adv.ManufacturerHex)
	}
}

func TestParseRSSIFromSignalPower(t *testing.T) {
	payload := advABytes(sensorMAC)
	pkt := blePacket(-95, advAA, 2, false, payload) // ADV_NONCONN_IND

	adv, ok := Parse(pkt)
	if !ok {
		t.Fatalf("Parse returned ok=false")
	}
	if adv.PDUType != "ADV_NONCONN_IND" {
		t.Errorf("PDUType = %q, want ADV_NONCONN_IND", adv.PDUType)
	}
	if adv.RSSI == nil || *adv.RSSI != -95 {
		t.Errorf("RSSI = %v, want -95", adv.RSSI)
	}
}
