package wifi

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jbrahy/rfmon/internal/pcap"
	"github.com/jbrahy/rfmon/internal/store"
)

// framesBufferSize is the capacity of the Supervisor's parsed-frame
// channel. Once full, the reader goroutine drops the oldest queued frame
// rather than blocking on a slow consumer.
const framesBufferSize = 256

// Decoder configures the external GNU Radio OFDM decoder process that the
// Supervisor runs: Python is the interpreter (or a wrapper such as
// /bin/sh), Script is the decoder's entry point, ModulePath is exported as
// PYTHONPATH, and LibPath is exported as DYLD_LIBRARY_PATH so the process
// can find gr-foo's shared libraries.
type Decoder struct {
	Python     string
	Script     string
	ModulePath string
	LibPath    string
}

// TimedFrame pairs a parsed WiFi management frame with the time the
// Supervisor read it from the decoder's stdout. Attribution to a specific
// dwell window is done by the scheduler, by comparing At against the
// dwell's start and end times.
type TimedFrame struct {
	Frame
	At time.Time
}

// Supervisor runs one long-running GNU Radio OFDM decoder process, feeds
// it IQ samples on stdin, and parses management frames from its radiotap
// pcap stdout. It is safe for concurrent use.
type Supervisor struct {
	// Stderr receives the decoder process's stderr output. If nil,
	// Start defaults it to os.Stderr.
	Stderr io.Writer

	d Decoder

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	waitDone chan struct{}
	exited   bool
	exitErr  error

	frames chan TimedFrame

	dropMu      sync.Mutex
	dropCount   int
	lastDropLog time.Time
}

// NewSupervisor returns a Supervisor for the given decoder configuration.
// Call Start to launch the process.
func NewSupervisor(d Decoder) *Supervisor {
	return &Supervisor{
		d:      d,
		frames: make(chan TimedFrame, framesBufferSize),
	}
}

// Start launches the decoder process and begins reading its stdout pcap
// in a background goroutine.
func (s *Supervisor) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked()
}

// startLocked launches the decoder process. s.mu must be held.
func (s *Supervisor) startLocked() error {
	cmd := exec.Command(s.d.Python, s.d.Script)
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+s.d.ModulePath,
		"DYLD_LIBRARY_PATH="+s.d.LibPath,
	)
	stderr := s.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd.Stderr = stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("wifi: decoder stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("wifi: decoder stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("wifi: starting decoder: %w", err)
	}

	waitDone := make(chan struct{})
	s.cmd = cmd
	s.stdin = stdin
	s.waitDone = waitDone
	s.exited = false
	s.exitErr = nil

	go s.run(cmd, stdout, waitDone)
	return nil
}

// run reads pcap records from stdout, parses each into a TimedFrame, and
// sends it on s.frames. It exits its read loop on EOF or a stream error,
// then reaps the process with Wait and records whether it has exited.
// Reading and Wait share one goroutine per run so Wait is never called
// concurrently with an in-progress stdout read, per exec.Cmd's
// StdinPipe/StdoutPipe documentation.
func (s *Supervisor) run(cmd *exec.Cmd, stdout io.Reader, waitDone chan struct{}) {
	pr, _, err := pcap.NewReader(stdout)
	if err == nil {
		for {
			pkt, err := pr.Next()
			if err != nil {
				break
			}
			frame, ok := Parse(pkt.Data)
			if !ok {
				continue
			}
			s.send(TimedFrame{Frame: frame, At: time.Now()})
		}
	}

	waitErr := cmd.Wait()

	s.mu.Lock()
	s.exited = true
	s.exitErr = waitErr
	s.mu.Unlock()

	close(waitDone)
}

// send delivers tf on s.frames, dropping the oldest queued frame instead
// of blocking when the channel is full, so a slow consumer cannot stall
// the pcap reader. Drops are counted and logged at most once per minute.
func (s *Supervisor) send(tf TimedFrame) {
	select {
	case s.frames <- tf:
		return
	default:
	}

	select {
	case <-s.frames:
	default:
	}
	select {
	case s.frames <- tf:
	default:
	}

	s.recordDrop()
}

func (s *Supervisor) recordDrop() {
	s.dropMu.Lock()
	s.dropCount++
	n := s.dropCount
	logNow := time.Since(s.lastDropLog) >= time.Minute
	if logNow {
		s.lastDropLog = time.Now()
	}
	s.dropMu.Unlock()

	if logNow {
		log.Printf("wifi: decoder consumer too slow, dropped %d frames total", n)
	}
}

// Stdin returns the decoder process's stdin, for writing IQ samples.
func (s *Supervisor) Stdin() io.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stdin
}

// Frames returns the channel of parsed management frames, each stamped
// with the time it was read.
func (s *Supervisor) Frames() <-chan TimedFrame {
	return s.frames
}

// Ensure restarts the decoder process if it has exited. It is a no-op if
// the process is still running.
func (s *Supervisor) Ensure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.exited {
		return nil
	}
	return s.startLocked()
}

// Close shuts down the decoder process: it closes stdin (which the fake
// and real decoders both treat as a signal to exit), waits briefly for a
// clean exit, then escalates to SIGTERM and finally SIGKILL if the process
// does not exit on its own.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	cmd := s.cmd
	stdin := s.stdin
	waitDone := s.waitDone
	s.mu.Unlock()

	if stdin != nil {
		stdin.Close()
	}
	if cmd == nil {
		return nil
	}

	select {
	case <-waitDone:
		return nil
	case <-time.After(3 * time.Second):
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-waitDone:
		return nil
	case <-time.After(2 * time.Second):
	}

	_ = cmd.Process.Kill()
	<-waitDone
	return nil
}

// TransferRunner runs hackrf_transfer for one dwell, writing raw IQ
// samples to a writer.
type TransferRunner struct {
	Bin string
}

// dwellSampleRate is the sample rate (Hz) passed to hackrf_transfer via -s.
const dwellSampleRate = 20_000_000

// Dwell runs hackrf_transfer for dur, tuned to freqHz, writing its IQ
// output to w. Canceling ctx sends SIGINT to the process and allows up to
// WaitDelay for it to exit before Run forcibly kills it.
func (r TransferRunner) Dwell(ctx context.Context, w io.Writer, freqHz int64, dur time.Duration) error {
	n := int64(dur.Seconds()) * dwellSampleRate

	cmd := exec.CommandContext(ctx, r.Bin,
		"-r", "-",
		"-f", strconv.FormatInt(freqHz, 10),
		"-s", strconv.Itoa(dwellSampleRate),
		"-l", "40",
		"-g", "30",
		"-a", "1",
		"-n", strconv.FormatInt(n, 10),
	)
	cmd.Stdout = w
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGINT)
	}
	cmd.WaitDelay = 3 * time.Second

	return cmd.Run()
}

// aggKey groups frames for Aggregator by device, frame type, and SSID.
type aggKey struct {
	mac       string
	frameType string
	hasSSID   bool
	ssid      string
}

// aggEntry accumulates one group of frames into a pending store.WifiSighting.
type aggEntry struct {
	sighting store.WifiSighting
}

// Aggregator groups parsed WiFi frames from one dwell into
// store.WifiSighting rows, keyed by (MAC, FrameType, SSID). It is not safe
// for concurrent use.
type Aggregator struct {
	order   []aggKey
	entries map[aggKey]*aggEntry
}

// NewAggregator returns an empty Aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{entries: make(map[aggKey]*aggEntry)}
}

// Add folds one parsed frame into its group, incrementing the group's
// frame count and raising its best SNR if f's SNR is higher.
func (a *Aggregator) Add(f Frame) {
	key := aggKey{mac: f.MAC, frameType: f.FrameType}
	if f.SSID != nil {
		key.hasSSID = true
		key.ssid = *f.SSID
	}

	e, ok := a.entries[key]
	if !ok {
		e = &aggEntry{sighting: store.WifiSighting{
			MAC:        f.MAC,
			DeviceKind: f.Kind,
			SSID:       f.SSID,
			Channel:    f.Channel,
			Security:   f.Security,
			Randomized: f.Randomized,
			FrameType:  f.FrameType,
			Decoder:    "ofdm",
		}}
		a.entries[key] = e
		a.order = append(a.order, key)
	}

	e.sighting.FrameCount++
	if f.SNR != nil {
		if e.sighting.BestSNR == nil || *f.SNR > *e.sighting.BestSNR {
			snr := *f.SNR
			e.sighting.BestSNR = &snr
		}
	}
}

// Drain returns one store.WifiSighting per group accumulated since the
// last Drain (or since NewAggregator), then resets the Aggregator.
func (a *Aggregator) Drain() []store.WifiSighting {
	result := make([]store.WifiSighting, 0, len(a.order))
	for _, key := range a.order {
		result = append(result, a.entries[key].sighting)
	}
	a.order = nil
	a.entries = make(map[aggKey]*aggEntry)
	return result
}
