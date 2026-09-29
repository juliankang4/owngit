package server

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"owngit/internal/importsync"
	"owngit/internal/tailscale"
)

// tailscaleReadingTTL is how long one reading of Tailscale's state serves
// further reports. The Settings page reports on every view, for any viewer
// with general access, so reports share one reading in flight and reuse it
// for this long: however many requests arrive, at most one status and one
// Serve configuration read run at a time, and none again within the TTL.
const tailscaleReadingTTL = 3 * time.Second

// tailscaleReading is what a report reads from Tailscale's LocalAPI: the
// status and the Serve configuration.
type tailscaleReading struct {
	command    tailscale.Command
	commandErr error
	status     tailscale.Status
	statusErr  error
	config     tailscale.ServeConfig
	configErr  error
}

// readingInFlight is a reading that runs in the background. done is closed
// when it finished, and reading is then what it read.
type readingInFlight struct {
	done       chan struct{}
	generation uint64
	reading    tailscaleReading
}

// readingCache holds the latest reading and the one in flight.
type readingCache struct {
	mu      sync.Mutex
	reading tailscaleReading
	at      time.Time
	// pending is the reading in flight, or nil.
	pending *readingInFlight
	// generation changes when forget drops the reading, so a reading that
	// was in flight across a change is not kept.
	generation uint64
	// addresses are this computer's Tailscale addresses from the latest
	// reading, none when it could not tell; known is set once a reading
	// finished. refreshed is closed when the reading that addresses
	// started in the background finishes, and is nil when none runs.
	addresses []netip.Addr
	known     bool
	refreshed chan struct{}
	// life is the lifetime of the background readings, which Stop ends:
	// their commands are stopped, running counts the readings until they
	// return, and stopped keeps new ones from starting.
	life    context.Context
	end     context.CancelFunc
	stopped bool
	running sync.WaitGroup
}

// errReadingUnfinished marks a report whose Tailscale reading did not finish
// before the request's deadline, which is Tailscale not answering in time,
// not a failure of OwnGit's own reads.
var errReadingUnfinished = errors.New("the Tailscale reading did not finish in time")

// errReadingsStopped is the answer to a report once the server stopped its
// readings, as it stops serving.
var errReadingsStopped = fmt.Errorf("Tailscale readings: %w", importsync.ErrShuttingDown)

// tailnetLabelWait bounds how long a page waits for this computer's
// Tailscale addresses before the first reading has finished. Once one has,
// pages use the latest addresses and never wait (addresses).
const tailnetLabelWait = time.Second

// read returns a reading no older than tailscaleReadingTTL. A request never
// waits for Tailscale longer than its own deadline: the reading runs in the
// background, shared with every report that arrives meanwhile, and read
// returns ctx's error when ctx ends first. The reading then still finishes
// within each command's own time limit and serves the next report.
func (sharing *Tailscale) read(ctx context.Context) (tailscaleReading, error) {
	cache := &sharing.readings
	cache.mu.Lock()
	if cache.stopped {
		cache.mu.Unlock()
		return tailscaleReading{}, errReadingsStopped
	}
	if !cache.at.IsZero() && time.Since(cache.at) < tailscaleReadingTTL {
		reading := cache.reading
		cache.mu.Unlock()
		return reading, nil
	}
	// A reading that started before a change (forget) may show Tailscale as
	// it was, so it is not joined; the new one runs after it, so the
	// commands of one reading still run one at a time.
	flight := cache.pending
	if flight == nil || flight.generation != cache.generation {
		var earlier <-chan struct{}
		if flight != nil {
			earlier = flight.done
		}
		flight = &readingInFlight{done: make(chan struct{}), generation: cache.generation}
		cache.pending = flight
		if cache.life == nil {
			cache.life, cache.end = context.WithCancel(context.Background())
		}
		cache.running.Add(1)
		go sharing.finish(cache.life, flight, earlier)
	}
	cache.mu.Unlock()
	select {
	case <-flight.done:
		return flight.reading, nil
	case <-ctx.Done():
		return tailscaleReading{}, fmt.Errorf("%w: %w", errReadingUnfinished, ctx.Err())
	}
}

// finish runs the reading in flight under life, after the earlier one when
// it is not nil, and keeps it unless a change dropped the readings meanwhile.
func (sharing *Tailscale) finish(life context.Context, flight *readingInFlight, earlier <-chan struct{}) {
	defer sharing.readings.running.Done()
	if earlier != nil {
		<-earlier
	}
	reading := sharing.readNow(life)
	cache := &sharing.readings
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.generation == flight.generation {
		cache.reading, cache.at = reading, time.Now()
	}
	cache.addresses, cache.known = nil, true
	if reading.commandErr == nil && reading.statusErr == nil {
		cache.addresses = reading.status.Addresses
	}
	if cache.pending == flight {
		cache.pending = nil
	}
	flight.reading = reading
	close(flight.done)
}

// addresses returns this computer's Tailscale addresses for the connection
// label of a page, without waiting for Tailscale, which can take up to its
// time limit when tailscaled does not answer. When the
// latest reading is older than tailscaleReadingTTL, it starts one in the
// background, whose addresses later pages use. Only before the first
// reading has finished does it wait for it, up to tailnetLabelWait.
func (sharing *Tailscale) addresses() []netip.Addr {
	cache := &sharing.readings
	cache.mu.Lock()
	due := cache.at.IsZero() || time.Since(cache.at) >= tailscaleReadingTTL
	if due && cache.refreshed == nil {
		done := make(chan struct{})
		cache.refreshed = done
		go func() {
			_, _ = sharing.read(context.Background())
			cache.mu.Lock()
			cache.refreshed = nil
			cache.mu.Unlock()
			close(done)
		}()
	}
	known, addresses, refreshed := cache.known, cache.addresses, cache.refreshed
	cache.mu.Unlock()
	if known || refreshed == nil {
		return addresses
	}
	timer := time.NewTimer(tailnetLabelWait)
	defer timer.Stop()
	select {
	case <-refreshed:
	case <-timer.C:
		return nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.addresses
}

// readNow runs the reads of one reading.
func (sharing *Tailscale) readNow(ctx context.Context) tailscaleReading {
	var reading tailscaleReading
	reading.command, reading.commandErr = sharing.Find()
	if reading.commandErr != nil {
		return reading
	}
	reading.status, reading.statusErr = reading.command.Status(ctx)
	reading.config, reading.configErr = reading.command.ServeConfig(ctx)
	return reading
}

// Stop ends the background readings when the server stops: their tailscale
// commands are stopped, and Stop waits for the readings to return until ctx
// ends. Reports after Stop get errReadingsStopped.
func (sharing *Tailscale) Stop(ctx context.Context) error {
	cache := &sharing.readings
	cache.mu.Lock()
	cache.stopped = true
	if cache.end != nil {
		cache.end()
	}
	cache.mu.Unlock()
	returned := make(chan struct{})
	go func() {
		cache.running.Wait()
		close(returned)
	}()
	select {
	case <-returned:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// forget drops the kept reading after a change, so the next report reads
// Tailscale again.
func (sharing *Tailscale) forget() {
	cache := &sharing.readings
	cache.mu.Lock()
	cache.at = time.Time{}
	cache.generation++
	cache.mu.Unlock()
}
