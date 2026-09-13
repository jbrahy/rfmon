package pcap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"
)

const (
	magicLE = 0xa1b2c3d4
	magicBE = 0xd4c3b2a1
)

// buildGlobalHeader writes a 24 byte classic pcap global header using the
// given byte order and magic number.
func buildGlobalHeader(buf *bytes.Buffer, order binary.ByteOrder, magic uint32, network uint32) {
	binary.Write(buf, order, magic)
	binary.Write(buf, order, uint16(2))     // version_major
	binary.Write(buf, order, uint16(4))     // version_minor
	binary.Write(buf, order, int32(0))      // thiszone
	binary.Write(buf, order, uint32(0))     // sigfigs
	binary.Write(buf, order, uint32(65535)) // snaplen
	binary.Write(buf, order, network)       // network (link type)
}

// buildRecord writes a 16 byte record header followed by the payload.
func buildRecord(buf *bytes.Buffer, order binary.ByteOrder, sec, usec uint32, payload []byte) {
	binary.Write(buf, order, sec)
	binary.Write(buf, order, usec)
	binary.Write(buf, order, uint32(len(payload)))
	binary.Write(buf, order, uint32(len(payload)))
	buf.Write(payload)
}

func TestNewReaderAndNextLittleEndian(t *testing.T) {
	var buf bytes.Buffer
	buildGlobalHeader(&buf, binary.LittleEndian, magicLE, 127)

	pkt1 := []byte{0x01, 0x02, 0x03}
	pkt2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	buildRecord(&buf, binary.LittleEndian, 1700000000, 500000, pkt1)
	buildRecord(&buf, binary.LittleEndian, 1700000001, 123456, pkt2)

	r, network, err := NewReader(&buf)
	if err != nil {
		t.Fatalf("NewReader returned error: %v", err)
	}
	if network != 127 {
		t.Fatalf("network = %d, want 127", network)
	}

	p1, err := r.Next()
	if err != nil {
		t.Fatalf("first Next returned error: %v", err)
	}
	wantTime1 := time.Unix(1700000000, 500000*1000).UTC()
	if !p1.Time.Equal(wantTime1) {
		t.Errorf("p1.Time = %v, want %v", p1.Time, wantTime1)
	}
	if !bytes.Equal(p1.Data, pkt1) {
		t.Errorf("p1.Data = %x, want %x", p1.Data, pkt1)
	}

	p2, err := r.Next()
	if err != nil {
		t.Fatalf("second Next returned error: %v", err)
	}
	wantTime2 := time.Unix(1700000001, 123456*1000).UTC()
	if !p2.Time.Equal(wantTime2) {
		t.Errorf("p2.Time = %v, want %v", p2.Time, wantTime2)
	}
	if !bytes.Equal(p2.Data, pkt2) {
		t.Errorf("p2.Data = %x, want %x", p2.Data, pkt2)
	}

	_, err = r.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("third Next err = %v, want io.EOF", err)
	}
}

func TestNextTruncatedRecordHeader(t *testing.T) {
	var buf bytes.Buffer
	buildGlobalHeader(&buf, binary.LittleEndian, magicLE, 127)
	// Write only 5 bytes of what should be a 16 byte record header.
	buf.Write([]byte{0x01, 0x02, 0x03, 0x04, 0x05})

	r, _, err := NewReader(&buf)
	if err != nil {
		t.Fatalf("NewReader returned error: %v", err)
	}

	_, err = r.Next()
	if err == nil {
		t.Fatal("Next returned nil error for truncated record header")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("Next err = %v, want non-EOF error", err)
	}
}

func TestNextTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	buildGlobalHeader(&buf, binary.LittleEndian, magicLE, 127)
	// Record header claims 10 bytes of payload but only 3 are written.
	binary.Write(&buf, binary.LittleEndian, uint32(1700000000))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(10))
	binary.Write(&buf, binary.LittleEndian, uint32(10))
	buf.Write([]byte{0x01, 0x02, 0x03})

	r, _, err := NewReader(&buf)
	if err != nil {
		t.Fatalf("NewReader returned error: %v", err)
	}

	_, err = r.Next()
	if err == nil {
		t.Fatal("Next returned nil error for truncated payload")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("Next err = %v, want non-EOF error", err)
	}
}

func TestNewReaderBigEndian(t *testing.T) {
	var buf bytes.Buffer
	// The on-disk magic value is always the conceptual 0xa1b2c3d4; writing
	// it with BigEndian order here produces the byte pattern a real
	// big-endian pcap file has, which NewReader must recognize as swapped.
	buildGlobalHeader(&buf, binary.BigEndian, magicLE, 127)

	pkt := []byte{0x11, 0x22, 0x33}
	buildRecord(&buf, binary.BigEndian, 1700000000, 250000, pkt)

	r, network, err := NewReader(&buf)
	if err != nil {
		t.Fatalf("NewReader returned error: %v", err)
	}
	if network != 127 {
		t.Fatalf("network = %d, want 127", network)
	}

	p, err := r.Next()
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	wantTime := time.Unix(1700000000, 250000*1000).UTC()
	if !p.Time.Equal(wantTime) {
		t.Errorf("p.Time = %v, want %v", p.Time, wantTime)
	}
	if !bytes.Equal(p.Data, pkt) {
		t.Errorf("p.Data = %x, want %x", p.Data, pkt)
	}

	_, err = r.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second Next err = %v, want io.EOF", err)
	}
}

func TestNewReaderUnknownMagic(t *testing.T) {
	var buf bytes.Buffer
	buildGlobalHeader(&buf, binary.LittleEndian, 0xdeadbeef, 127)

	_, _, err := NewReader(&buf)
	if err == nil {
		t.Fatal("NewReader returned nil error for unknown magic")
	}
}
