// Package pcap provides a streaming reader for classic (not pcapng) pcap
// files, as produced by tools such as tcpdump. It is used by the WiFi
// decoder (link type 127, radiotap) and BLE capture (link type 256).
package pcap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	magicLittleEndian = 0xa1b2c3d4
	magicBigEndian    = 0xd4c3b2a1

	globalHeaderLen = 24
	recordHeaderLen = 16

	// maxRecordLen is the largest incl_len NewReader will honor when the
	// global header's snaplen is zero or larger than this. 1 MiB is well
	// beyond any radiotap WiFi or BLE HCI capture frame.
	maxRecordLen = 1 << 20
)

// Packet is a single record read from a pcap stream.
type Packet struct {
	Time time.Time
	Data []byte
}

// Reader reads packets from a classic pcap stream one at a time.
type Reader struct {
	r         io.Reader
	order     binary.ByteOrder
	maxRecLen uint32
}

// NewReader reads and validates the 24 byte global pcap header from r and
// returns a Reader positioned at the first record, along with the
// link-layer type (network field) from the header.
func NewReader(r io.Reader) (*Reader, uint32, error) {
	var hdr [globalHeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, 0, fmt.Errorf("pcap: reading global header: %w", err)
	}

	magic := binary.LittleEndian.Uint32(hdr[0:4])
	var order binary.ByteOrder
	switch magic {
	case magicLittleEndian:
		order = binary.LittleEndian
	case magicBigEndian:
		order = binary.BigEndian
	default:
		return nil, 0, fmt.Errorf("pcap: unknown magic number %#x", magic)
	}

	snaplen := order.Uint32(hdr[16:20])
	network := order.Uint32(hdr[20:24])

	recLen := uint32(maxRecordLen)
	if snaplen > 0 && snaplen < maxRecordLen {
		recLen = snaplen
	}

	return &Reader{r: r, order: order, maxRecLen: recLen}, network, nil
}

// Next reads and returns the next packet from the stream. It returns io.EOF
// when the stream ends cleanly between records. A record header or payload
// that is truncated mid-record returns a non-EOF error.
func (r *Reader) Next() (Packet, error) {
	var hdr [recordHeaderLen]byte
	if _, err := io.ReadFull(r.r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return Packet{}, io.EOF
		}
		return Packet{}, fmt.Errorf("pcap: reading record header: %w", err)
	}

	sec := r.order.Uint32(hdr[0:4])
	usec := r.order.Uint32(hdr[4:8])
	inclLen := r.order.Uint32(hdr[8:12])

	if inclLen > r.maxRecLen {
		return Packet{}, fmt.Errorf("pcap: record length %d exceeds bound %d", inclLen, r.maxRecLen)
	}

	data := make([]byte, inclLen)
	if _, err := io.ReadFull(r.r, data); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Packet{}, fmt.Errorf("pcap: reading record payload: %w", err)
	}

	return Packet{
		Time: time.Unix(int64(sec), int64(usec)*1000).UTC(),
		Data: data,
	}, nil
}
