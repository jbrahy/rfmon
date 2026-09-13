// Package ble parses ice9-bluetooth's LE pseudo-header packets (pcap link
// type 256, BLUETOOTH_LE_LL_WITH_PHDR) into a summary of a BLE advertising
// PDU.
package ble

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// lePhdrLen is the size of ice9-bluetooth's LE pseudo-header: rf_channel
// (u8), signal_power (i8, dBm), noise_power (i8), aa_offenses (u8), ref_aa
// (u32), flags (u16). All fields are packed little-endian.
const lePhdrLen = 10

// signalPowerOffset is the byte offset of the signal power (dBm) field
// within the pseudo-header.
const signalPowerOffset = 1

// accessAddressOffset is the byte offset of the 4 byte access address that
// follows the pseudo-header.
const accessAddressOffset = lePhdrLen

// pduHeaderOffset is the byte offset of the PDU header byte (type in the
// low 4 bits, TxAdd in bit 6) that follows the access address.
const pduHeaderOffset = accessAddressOffset + 4

// pduLengthOffset is the byte offset of the PDU length byte that follows
// the header byte.
const pduLengthOffset = pduHeaderOffset + 1

// payloadOffset is the byte offset of the PDU payload.
const payloadOffset = pduLengthOffset + 1

// advertisingAccessAddress is the fixed access address used on the three
// primary advertising channels.
const advertisingAccessAddress = 0x8E89BED6

// advATxAddBit is bit 6 of the PDU header byte.
const advATxAddBit = 1 << 6

// advALen is the length of the AdvA field (a Bluetooth device address) at
// the start of the payload of the PDU types this package records.
const advALen = 6

// Advertising PDU types (low 4 bits of the header byte).
const (
	pduADVInd        = 0
	pduADVDirectInd  = 1
	pduADVNonconnInd = 2
	pduScanReq       = 3
	pduScanRsp       = 4
	pduConnectInd    = 5
	pduADVScanInd    = 6
	pduADVExtInd     = 7
)

// pduTypeNames names the PDU types this package records. Everything else
// (SCAN_REQ, ADV_DIRECT_IND, CONNECT_IND, and ADV_EXT_IND) is rejected by
// Parse; see the comment there.
var pduTypeNames = map[byte]string{
	pduADVInd:        "ADV_IND",
	pduADVNonconnInd: "ADV_NONCONN_IND",
	pduScanRsp:       "SCAN_RSP",
	pduADVScanInd:    "ADV_SCAN_IND",
}

// AD structure types this package understands.
const (
	adLocalNameShort    = 0x08
	adLocalNameComplete = 0x09
	adUUID16Incomplete  = 0x02
	adUUID16Complete    = 0x03
	adManufacturer      = 0xFF
	adTxPower           = 0x0A
)

// Adv summarizes a BLE advertising PDU.
type Adv struct {
	Address         string
	AddressType     string // "public", "random_static", "random_resolvable", "random_nonresolvable"
	PDUType         string
	Name            *string
	CompanyID       *int
	ManufacturerHex *string
	ServiceUUIDs    []string
	TxPower         *int
	RSSI            *int
}

// Parse parses an ice9-bluetooth LE pseudo-header packet into an Adv
// summary. It returns ok=false for anything that is not one of the four
// advertising PDU types it records (ADV_IND, ADV_NONCONN_IND, SCAN_RSP,
// ADV_SCAN_IND), for a non-advertising access address, or for a packet too
// short to hold its declared fields.
//
// ADV_EXT_IND (type 7) is always rejected: its payload uses an extended
// header with an AdvA field that is optional and, when present, sits behind
// a variable-length extended header rather than at a fixed offset. Parsing
// that is out of scope for this task.
func Parse(lePhdrPacket []byte) (Adv, bool) {
	if len(lePhdrPacket) < payloadOffset {
		return Adv{}, false
	}

	signalPower := int8(lePhdrPacket[signalPowerOffset])

	aa := binary.LittleEndian.Uint32(lePhdrPacket[accessAddressOffset : accessAddressOffset+4])
	if aa != advertisingAccessAddress {
		return Adv{}, false
	}

	header := lePhdrPacket[pduHeaderOffset]
	pduType := header & 0x0F
	txAdd := header&advATxAddBit != 0

	pduTypeName, ok := pduTypeNames[pduType]
	if !ok {
		return Adv{}, false
	}

	payloadLen := int(lePhdrPacket[pduLengthOffset])
	if len(lePhdrPacket) < payloadOffset+payloadLen {
		return Adv{}, false
	}
	payload := lePhdrPacket[payloadOffset : payloadOffset+payloadLen]

	if len(payload) < advALen {
		return Adv{}, false
	}
	advA := payload[:advALen]

	addressType, ok := addressType(txAdd, advA)
	if !ok {
		return Adv{}, false
	}

	adv := Adv{
		Address:     formatAdvA(advA),
		AddressType: addressType,
		PDUType:     pduTypeName,
		RSSI:        intPtr(int(signalPower)),
	}

	parseAD(payload[advALen:], &adv)

	return adv, true
}

// addressType derives the address type from the TxAdd bit and, for random
// addresses, the top two bits of the most significant address octet (the
// last byte of the on-air AdvA field).
func addressType(txAdd bool, advA []byte) (string, bool) {
	if !txAdd {
		return "public", true
	}
	switch advA[advALen-1] >> 6 {
	case 0b11:
		return "random_static", true
	case 0b01:
		return "random_resolvable", true
	case 0b00:
		return "random_nonresolvable", true
	default:
		return "", false // 0b10 is reserved
	}
}

// formatAdvA formats the six AdvA bytes reversed (the on-air field is
// little-endian), as lowercase colon-separated hex.
func formatAdvA(advA []byte) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		advA[5], advA[4], advA[3], advA[2], advA[1], advA[0])
}

// parseAD walks the AD structures following AdvA, filling in adv. Each
// structure is a length byte (covering the type byte and value), a type
// byte, and a value. A length that runs past the remaining data stops the
// walk without touching adv further; everything parsed up to that point is
// kept.
func parseAD(data []byte, adv *Adv) {
	pos := 0
	for pos < len(data) {
		adLen := int(data[pos])
		if adLen == 0 {
			break
		}
		dataStart := pos + 1
		dataEnd := dataStart + adLen
		if dataEnd > len(data) {
			break // length runs past the payload; malformed, stop here
		}
		adType := data[dataStart]
		value := data[dataStart+1 : dataEnd]

		switch adType {
		case adLocalNameShort, adLocalNameComplete:
			s := string(value)
			adv.Name = &s
		case adManufacturer:
			if len(value) >= 2 {
				companyID := int(binary.LittleEndian.Uint16(value[0:2]))
				adv.CompanyID = &companyID
				h := hex.EncodeToString(value)
				adv.ManufacturerHex = &h
			}
		case adUUID16Incomplete, adUUID16Complete:
			for i := 0; i+2 <= len(value); i += 2 {
				uuid := binary.LittleEndian.Uint16(value[i : i+2])
				adv.ServiceUUIDs = append(adv.ServiceUUIDs, fmt.Sprintf("%04x", uuid))
			}
		case adTxPower:
			if len(value) >= 1 {
				tp := int(int8(value[0]))
				adv.TxPower = &tp
			}
		}

		pos = dataEnd
	}
}

func intPtr(v int) *int {
	return &v
}
