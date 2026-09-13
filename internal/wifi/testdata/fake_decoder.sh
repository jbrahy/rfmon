#!/bin/sh
# Test fixture standing in for the GNU Radio OFDM decoder process.
#
# It discards stdin in the background (mirroring the real decoder, which
# consumes IQ samples from stdin), writes a single synthetic radiotap pcap
# beacon frame to stdout, and then stays alive until stdin closes so the
# supervisor's Close() has something to shut down cleanly.
#
# No real captured SSID or MAC appears here; all values below are made up
# for the test.

cat >/dev/null &
reader_pid=$!

python3 - <<'PY'
import struct
import sys

out = bytearray()

# pcap global header (24 bytes, little-endian), link type 127 (radiotap).
out += struct.pack('<IHHiIII', 0xa1b2c3d4, 2, 4, 0, 0, 65535, 127)

# Minimal 17 byte radiotap header (gr-foo's fixed layout): version, pad,
# length, present bitmask (signal bit only), then padding up to the
# antenna signal byte at offset 14.
radiotap = bytearray(17)
struct.pack_into('<H', radiotap, 2, 17)          # length
struct.pack_into('<I', radiotap, 4, 0x00000020)  # present: antenna signal
radiotap[14] = 200                               # SNR (dB)

# 802.11 beacon frame: header (24 bytes) + fixed params (12 bytes) +
# SSID information element.
dot11 = bytearray()
dot11 += bytes([0x80, 0x00])                           # frame control: beacon
dot11 += bytes([0x00, 0x00])                           # duration
dot11 += bytes([0xff] * 6)                             # addr1: broadcast
dot11 += bytes([0x02, 0x11, 0x22, 0x33, 0x44, 0x55])   # addr2: transmitter
dot11 += bytes([0x02, 0x11, 0x22, 0x33, 0x44, 0x66])   # addr3: BSSID
dot11 += bytes([0x00, 0x00])                           # sequence control
dot11 += bytes(8)                                      # timestamp
dot11 += struct.pack('<H', 0x0064)                     # beacon interval
dot11 += struct.pack('<H', 0x0001)                     # capability info (ESS)
ssid = b'FIXTURE-NET'
dot11 += bytes([0x00, len(ssid)]) + ssid               # SSID element

frame = bytes(radiotap) + bytes(dot11)

# pcap record header (16 bytes) + payload.
out += struct.pack('<IIII', 1700000000, 0, len(frame), len(frame))
out += frame

sys.stdout.buffer.write(bytes(out))
sys.stdout.buffer.flush()
PY

# Stay alive until the supervisor closes our stdin, at which point the
# background `cat` above exits and this script follows it.
wait "$reader_pid"
