package ble

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jbrahy/rfmon/internal/pcap"
	"github.com/jbrahy/rfmon/internal/store"
)

// bleLinkType is the pcap link-layer type ice9-bluetooth writes:
// BLUETOOTH_LE_LL_WITH_PHDR.
const bleLinkType = 256

// Runner runs ice9-bluetooth for one dwell and turns the resulting capture
// into aggregated sightings.
type Runner struct {
	Bin string
}

// Dwell runs `ice9-bluetooth -l -c <center> -C <channels> -w <pcapPath>` for
// dur, stops it with SIGINT, then parses pcapPath (which must be pcap link
// type 256) into aggregated BLE sightings, one per distinct
// (Address, AddressType) seen. The pcap file is deleted before Dwell
// returns, whether or not it succeeds.
func (r Runner) Dwell(ctx context.Context, center, channels int, pcapPath string, dur time.Duration) ([]store.BleSighting, error) {
	runCtx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()

	cmd := exec.CommandContext(runCtx, r.Bin,
		"-l",
		"-c", strconv.Itoa(center),
		"-C", strconv.Itoa(channels),
		"-w", pcapPath,
	)
	cmd.Cancel = func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 3 * time.Second

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ble: starting %s: %w", r.Bin, err)
	}

	if err := cmd.Wait(); err != nil && runCtx.Err() == nil {
		// The process exited on its own, for a reason other than us
		// stopping it via the dur timeout or a canceled ctx: a real error.
		return nil, fmt.Errorf("ble: running %s: %w", r.Bin, err)
	}

	defer os.Remove(pcapPath)

	return parseDwellPcap(pcapPath)
}

// parseDwellPcap opens pcapPath, requires link type 256, parses each
// packet with Parse, and aggregates the results.
func parseDwellPcap(pcapPath string) ([]store.BleSighting, error) {
	f, err := os.Open(pcapPath)
	if err != nil {
		return nil, fmt.Errorf("ble: opening %s: %w", pcapPath, err)
	}
	defer f.Close()

	reader, network, err := pcap.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("ble: reading pcap header of %s: %w", pcapPath, err)
	}
	if network != bleLinkType {
		return nil, fmt.Errorf("ble: %s has link type %d, want %d", pcapPath, network, bleLinkType)
	}

	var advs []Adv
	for {
		pkt, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("ble: reading packet from %s: %w", pcapPath, err)
		}
		adv, ok := Parse(pkt.Data)
		if !ok {
			continue
		}
		advs = append(advs, adv)
	}

	return aggregate(advs), nil
}

// advGroupKey identifies one aggregated sighting group.
type advGroupKey struct {
	address     string
	addressType string
}

// aggregate groups advs by (Address, AddressType) into one BleSighting per
// group: PDU types seen are unioned into a sorted, comma-separated list;
// each optional field keeps the last non-nil value seen; PacketCount is the
// number of advs in the group; BestRSSI keeps the strongest (maximum, i.e.
// least negative) RSSI seen. Groups are returned in order of first
// appearance.
func aggregate(advs []Adv) []store.BleSighting {
	var order []advGroupKey
	sightings := map[advGroupKey]*store.BleSighting{}
	pduTypes := map[advGroupKey]map[string]bool{}

	for _, a := range advs {
		key := advGroupKey{address: a.Address, addressType: a.AddressType}

		s, ok := sightings[key]
		if !ok {
			s = &store.BleSighting{Address: a.Address, AddressType: a.AddressType}
			sightings[key] = s
			pduTypes[key] = map[string]bool{}
			order = append(order, key)
		}

		pduTypes[key][a.PDUType] = true
		s.PacketCount++

		if a.Name != nil {
			s.Name = a.Name
		}
		if a.CompanyID != nil {
			s.CompanyID = a.CompanyID
		}
		if a.ManufacturerHex != nil {
			s.ManufacturerHex = a.ManufacturerHex
		}
		if len(a.ServiceUUIDs) > 0 {
			joined := strings.Join(a.ServiceUUIDs, ",")
			s.ServiceUUIDs = &joined
		}
		if a.TxPower != nil {
			s.TxPower = a.TxPower
		}
		if a.RSSI != nil && (s.BestRSSI == nil || *a.RSSI > *s.BestRSSI) {
			s.BestRSSI = a.RSSI
		}
	}

	result := make([]store.BleSighting, 0, len(order))
	for _, key := range order {
		s := sightings[key]

		types := make([]string, 0, len(pduTypes[key]))
		for t := range pduTypes[key] {
			types = append(types, t)
		}
		sort.Strings(types)
		s.PDUTypes = strings.Join(types, ",")

		result = append(result, *s)
	}

	return result
}
