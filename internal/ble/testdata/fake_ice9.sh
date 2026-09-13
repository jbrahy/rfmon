#!/bin/bash
# fake_ice9.sh stands in for ice9-bluetooth in tests. It ignores every flag
# except -w <path>: it writes a synthetic link-type-256 (BLE LE pseudo-header)
# pcap file containing two ADV_IND packets for the same synthetic address to
# that path, then waits to be signaled, just like a real live capture would.
set -e

pcap_path=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  if [[ "${args[$i]}" == "-w" ]]; then
    pcap_path="${args[$((i + 1))]}"
  fi
done

if [[ -z "$pcap_path" ]]; then
  echo "fake_ice9.sh: no -w path given" >&2
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
    for sec, usec in ((1700000000, 0), (1700000000, 1000)):
        f.write(struct.pack("<IIII", sec, usec, len(packet), len(packet)))
        f.write(packet)
PYEOF

# Wait to be signaled, then exit promptly. Two details matter for a prompt
# exit under load:
#   1. Install the trap before backgrounding anything, so there is no window
#      where a signal arrives before the handler is set and bash's default
#      action leaves an orphan behind.
#   2. Redirect the backgrounded sleep's descriptors to /dev/null so it can
#      never hold this process's stdout/stderr pipe open. Otherwise, if the
#      sleep is orphaned, the caller's Wait blocks on the still-open pipe for
#      the full WaitDelay before returning.
# Bash runs a trap only once the current foreground command finishes, so a
# `sleep`-based loop would delay the trap; waiting on a backgrounded child via
# the `wait` builtin lets the trap fire as soon as the signal arrives.
# (86400 = one day; plain "sleep infinity" is a GNU coreutils extension not
# present on macOS.)
child=""
trap 'if [[ -n "$child" ]]; then kill "$child" 2>/dev/null; fi; exit 0' INT TERM
sleep 86400 >/dev/null 2>&1 &
child=$!
# Signal readiness only after the trap is armed and the wait child exists, so
# a test that waits for this marker before canceling can never deliver the
# signal into the window before the handler is installed.
: > "${pcap_path}.ready"
wait "$child"
