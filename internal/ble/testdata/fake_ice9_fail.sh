#!/bin/bash
# fake_ice9_fail.sh simulates ice9-bluetooth crashing on its own before a
# dwell's dur elapses: it writes the same synthetic pcap as fake_ice9.sh,
# then exits non-zero immediately, without waiting for a signal.
set -e

pcap_path=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  if [[ "${args[$i]}" == "-w" ]]; then
    pcap_path="${args[$((i + 1))]}"
  fi
done

if [[ -z "$pcap_path" ]]; then
  echo "fake_ice9_fail.sh: no -w path given" >&2
  exit 1
fi

python3 - "$pcap_path" <<'PYEOF'
import struct
import sys

path = sys.argv[1]

# ice9-bluetooth's LE pseudo-header (10 bytes, little-endian): rf_channel
# (u8), signal_power (i8, dBm), noise_power (i8), aa_offenses (u8), ref_aa
# (u32), flags (u16).
le_phdr = struct.pack("<BbbBIH", 37, -60, -90, 0, 0x8E89BED6, 0)

# The 4 byte advertising access address that follows the pseudo-header.
access_address = struct.pack("<I", 0x8E89BED6)

# PDU header byte: type 0 (ADV_IND) in the low nibble, TxAdd (bit 6) clear
# for a public address.
pdu_header = bytes([0x00])

# A synthetic AdvA (device address), not a real captured address.
adv_a = bytes([0x01, 0x02, 0x03, 0x04, 0x05, 0x06])
pdu_length = bytes([len(adv_a)])

packet = le_phdr + access_address + pdu_header + pdu_length + adv_a

# Classic pcap global header: magic, version_major, version_minor,
# thiszone, sigfigs, snaplen, network (256 = BLUETOOTH_LE_LL_WITH_PHDR).
global_header = struct.pack("<IHHiIII", 0xA1B2C3D4, 2, 4, 0, 0, 65535, 256)

with open(path, "wb") as f:
    f.write(global_header)
    f.write(struct.pack("<IIII", 1700000000, 0, len(packet), len(packet)))
    f.write(packet)
PYEOF

# Simulate a crash: exit non-zero on its own, without waiting to be signaled.
exit 1
