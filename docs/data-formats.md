# Data formats

rfmon writes everything under the `-data` directory (default `./data`):

```
data/
  rrd/
    <slug>.rrd            one RRD file per band, 43 in total
  spectrum/
    bins.json             bin center frequencies for the snapshot records
    YYYY-MM-DD.bin.gz     full-spectrum snapshots for one UTC day
```

All timestamps are Unix seconds taken at the start of the poll.

## RRD files

One file per band, `data/rrd/<slug>.rrd`, created on the band's first
successful update. Slugs are listed in [bands.md](bands.md).

### Layout

The file is created with:

```
rrdtool create <slug>.rrd --start <first poll time - 60> --step 60 \
  DS:avg:GAUGE:180:-150:50 \
  DS:peak:GAUGE:180:-150:50 \
  DS:floor:GAUGE:180:-150:50 \
  DS:occ:GAUGE:180:0:100 \
  RRA:AVERAGE:0.5:1:2880    RRA:MAX:0.5:1:2880 \
  RRA:AVERAGE:0.5:5:4032    RRA:MAX:0.5:5:4032 \
  RRA:AVERAGE:0.5:30:2976   RRA:MAX:0.5:30:2976 \
  RRA:AVERAGE:0.5:120:8760  RRA:MAX:0.5:120:8760
```

- Step: 60 seconds.
- Heartbeat: 180 seconds. If more than 180 s pass between updates, the
  interval is stored as unknown.

Data sources, all `GAUGE`:

| DS | Unit | Allowed range | Meaning |
|---|---|---|---|
| `avg` | dB | -150 to 50 | Mean bin power, averaged in linear terms |
| `peak` | dB | -150 to 50 | Highest bin |
| `floor` | dB | -150 to 50 | 10th percentile bin |
| `occ` | percent | 0 to 100 | Bins at or above floor + 10 dB |

Values outside the allowed range are stored as unknown. Metric definitions
are in [bands.md](bands.md#metrics).

Round-robin archives. Each resolution exists twice, once consolidated with
AVERAGE and once with MAX. The xfiles factor is 0.5, so a consolidated row is
unknown if more than half of its primary points are unknown.

| RRA index | Function | Steps per row | Resolution | Rows | Retention |
|---|---|---:|---|---:|---|
| 0 | AVERAGE | 1 | 1 minute | 2880 | 2 days |
| 1 | MAX | 1 | 1 minute | 2880 | 2 days |
| 2 | AVERAGE | 5 | 5 minutes | 4032 | 14 days |
| 3 | MAX | 5 | 5 minutes | 4032 | 14 days |
| 4 | AVERAGE | 30 | 30 minutes | 2976 | 62 days |
| 5 | MAX | 30 | 30 minutes | 2976 | 62 days |
| 6 | AVERAGE | 120 | 2 hours | 8760 | 730 days (2 years) |
| 7 | MAX | 120 | 2 hours | 8760 | 730 days (2 years) |

Each file is 1,198,128 bytes and does not grow. The 43 files total about
51 MB.

Each poll writes one update of the form
`<unix seconds>:<avg>:<peak>:<floor>:<occ>`, each value with two decimals.
Polls arrive about every 65 seconds, not exactly every 60, so rrdtool
interpolates them onto the 60 second step.

### Graphs in the web UI

The built-in graphs use AVERAGE for `avg` and `floor` and MAX for `peak`, so a
week or year graph shows the highest peak within each consolidated row rather
than an average of peaks. `occ` is stored but not graphed by the web UI.

### Querying an RRD

Most recent update:

```sh
rrdtool lastupdate data/rrd/ism-2400.rrd
```

```
 avg peak floor occ

1789275182: -58.17 -39.86 -68.22 14.08
```

One-minute averages for the last hour:

```sh
rrdtool fetch data/rrd/fm-broadcast.rrd AVERAGE --start -1h
```

```
                            avg                peak               floor                 occ

1789275120: -2.1333000000e+01 -4.7865000000e+00 -4.6753000000e+01 1.5545500000e+01
1789275180: -2.1390000000e+01 -4.9100000000e+00 -4.6050000000e+01 9.7600000000e+00
...
```

Rows with no data print as `nan`. To read a coarser archive, ask for its
resolution in seconds, for example five-minute maximums over the last day:

```sh
rrdtool fetch data/rrd/fm-broadcast.rrd MAX --resolution 300 --start -1d
```

`rrdtool info data/rrd/fm-broadcast.rrd` prints the full header, including
every DS and RRA.

A custom graph of 2.4 GHz occupancy, which the web UI does not draw:

```sh
rrdtool graph ism-2400-occ.png --start -1d --width 600 --height 200 \
  --title "2.4 GHz ISM occupancy" --vertical-label "% of bins" \
  --lower-limit 0 --upper-limit 100 \
  DEF:occ=data/rrd/ism-2400.rrd:occ:AVERAGE \
  VDEF:occmax=occ,MAXIMUM \
  VDEF:occlast=occ,LAST \
  LINE2:occ#2ca02c:occupancy \
  "GPRINT:occlast:last %.1lf%%" \
  "GPRINT:occmax:max %.1lf%%"
```

The same `DEF` names work with `rrdtool xport --json` if you want the data as
JSON instead of a picture.

## Spectrum snapshots

### File naming and retention

Each poll appends one record to `data/spectrum/YYYY-MM-DD.bin.gz`, where the
date is the UTC date of the poll's start time. Outside UTC, the file name
changes at UTC midnight, not local midnight.

After every poll, rfmon deletes day files whose date is earlier than today's
UTC date minus 7 days. With today's file dated 2026-09-13, files from
2026-09-06 onward are kept and 2026-09-05 and earlier are deleted, so at most
8 files exist: today plus the 7 previous days. Files whose names do not parse
as a date are left alone, and `bins.json` is never deleted.

Measured size: about 17 KB per record compressed, roughly 20 to 25 MB per
day at the default pause, about 170 MB in steady state.

### Compression

Every record is written as its own gzip member, appended to the file. A
sequence of gzip members is a valid gzip stream, so standard tools read the
whole file:

```sh
gzip -dc data/spectrum/2026-09-13.bin.gz | wc -c
```

prints a multiple of 25212 (one uncompressed record with 25200 bins).

If rfmon is killed while writing, the last member can be truncated.
`spectrum.ReadDay` then returns the complete records before it along with an
error. Python's `gzip` module raises `EOFError` on such a file instead.

### Record layout

All integers are little-endian.

| Offset | Size | Type | Field |
|---:|---:|---|---|
| 0 | 8 | int64 | Poll start time, Unix seconds |
| 8 | 4 | uint32 | Bin count `N` (25200 for a full sweep) |
| 12 | N | uint8 x N | One byte per bin, in ascending frequency order |

A 25200 bin record is 25212 bytes before compression. Records follow each
other with no padding or separator.

Bin byte encoding:

| Byte | Meaning |
|---|---|
| 0 to 254 | Power: `dB = byte / 2 - 120` |
| 255 | No data: a spur-filtered bin, or a bin with no finite reading |

When writing, `byte = round((dB + 120) * 2)`, clamped to 0..254. The
resolution is 0.5 dB and the representable range is -120 dB to +7 dB. Values
below -120 dB are stored as 0 and values above +7 dB as 254.

The `i`th byte of a record corresponds to the `i`th frequency in `bins.json`.

### `bins.json`

A JSON array of integers: the bin center frequencies in Hz, ascending, one
per bin. It is rewritten only when the bin layout changes.

```
[1119000,1357000,1595000,1833000, ... ,6000405000,6000643000,6000881000]
```

For the fixed sweep arguments it has 25200 entries, from 1,119,000 Hz to
6,000,881,000 Hz. Centers are about 238 kHz apart (`hackrf_sweep` bins are
238095.24 Hz wide), and because they are rounded to the nearest kHz,
consecutive entries differ by either 238000 or 239000.

### Reading a day file in Go

`spectrum.ReadDay` decodes a day file into records with `DB` values as
`float64`, NaN for no-data bins. The package is internal to this module, so
the program must live inside a clone of the repository, for example as
`cmd/readday/main.go`, and run from the repository root:

```go
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"

	"github.com/jbrahy/rfmon/internal/spectrum"
)

func main() {
	dir := "data/spectrum"
	day := filepath.Join(dir, os.Args[1]+".bin.gz") // e.g. 2026-09-13

	raw, err := os.ReadFile(filepath.Join(dir, "bins.json"))
	if err != nil {
		log.Fatal(err)
	}
	var hz []int64
	if err := json.Unmarshal(raw, &hz); err != nil {
		log.Fatal(err)
	}

	recs, err := spectrum.ReadDay(day)
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range recs {
		peak, at := math.Inf(-1), int64(0)
		for i, db := range r.DB {
			if !math.IsNaN(db) && db > peak {
				peak, at = db, hz[i]
			}
		}
		fmt.Printf("%s peak %.1f dB at %.3f MHz\n", r.Time.Format("15:04:05"), peak, float64(at)/1e6)
	}
}
```

```sh
go run ./cmd/readday 2026-09-13
```

```
...
04:50:52 peak -5.0 dB at 88.024 MHz
04:51:57 peak -5.0 dB at 88.024 MHz
```

### Reading a day file in Python

No third-party packages are needed. `gzip.open` reads across all members.

```python
import gzip, json, struct, sys
from datetime import datetime, timezone

day_file, bins_file = sys.argv[1], sys.argv[2]
hz = json.load(open(bins_file))

with gzip.open(day_file, "rb") as f:  # reads all concatenated gzip members
    data = f.read()

pos = 0
while pos < len(data):
    ts, n = struct.unpack_from("<qI", data, pos)
    body = data[pos + 12 : pos + 12 + n]
    pos += 12 + n
    db = [None if q == 255 else q / 2 - 120 for q in body]
    valid = [(v, h) for v, h in zip(db, hz) if v is not None]
    peak_db, peak_hz = max(valid)
    when = datetime.fromtimestamp(ts, timezone.utc).isoformat()
    print(f"{when} bins={n} peak={peak_db:.1f} dB at {peak_hz / 1e6:.3f} MHz")
```

```sh
python3 readday.py data/spectrum/2026-09-13.bin.gz data/spectrum/bins.json
```

```
...
2026-09-13T04:50:52+00:00 bins=25200 peak=-5.0 dB at 88.024 MHz
2026-09-13T04:51:57+00:00 bins=25200 peak=-5.0 dB at 88.024 MHz
```
