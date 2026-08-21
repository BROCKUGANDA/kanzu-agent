// Package thermal implements the duty-cycle governor that keeps Kanzu Agent
// inside the ADTC thermal budget.
//
// ADTC deducts 10 points (P_thermal) if the CPU throttles or a core exceeds
// 85 °C. On a passively-or-barely-cooled $400 laptop, sustained all-core
// int8/int4 GEMM will reach that in well under a minute. Three mechanisms are
// combined here:
//
//  1. Thread capping (owned by config.defaultThreads): never use every core, so
//     the package never enters its highest-power state.
//  2. Duty cycling: after each inference burst, idle for a proportional interval.
//     A 0.7 duty cycle means 30% of wall time is idle, which on the reference
//     hardware is enough for the heat spreader to shed the burst.
//  3. Adaptive gating: before a burst, if a core is already at or above the
//     ceiling (default 82 °C, deliberately 3 °C below the penalty threshold),
//     block until it drops back to the resume temperature.
//
// On platforms with no readable sensor (cloud VMs, Windows dev boxes) gating
// degrades to duty cycling alone, which is the conservative direction: we idle
// anyway rather than assume the machine is cool.
package thermal

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Governor serialises and paces all LLM work. One Governor instance must be
// shared by every code path that can start inference; the mutex is what
// guarantees two bursts never overlap and double the power draw.
type Governor struct {
	CeilingC float64
	ResumeC  float64
	Duty     float64

	// Poll is how long to wait between temperature reads while gated.
	Poll time.Duration
	// MaxWait bounds a single gate so a stuck sensor cannot hang the agent.
	MaxWait time.Duration

	mu sync.Mutex

	statsMu       sync.Mutex
	peakC         float64
	pauses        int
	totalCooldown time.Duration
	totalBurst    time.Duration
	bursts        int
	sensorOK      bool
	sensorChecked bool
}

// New builds a Governor with sane bounds. Callers pass values from metadata.json.
func New(ceilingC, resumeC, duty float64) *Governor {
	if ceilingC <= 0 {
		ceilingC = 82.0
	}
	if resumeC <= 0 || resumeC >= ceilingC {
		resumeC = ceilingC - 10
	}
	if duty <= 0 || duty > 1 {
		duty = 0.7
	}
	return &Governor{
		CeilingC: ceilingC,
		ResumeC:  resumeC,
		Duty:     duty,
		Poll:     2 * time.Second,
		MaxWait:  90 * time.Second,
	}
}

// Stats is a snapshot of governor activity, surfaced by `kanzu chat :stats` and
// written into REPORT.md's thermal section.
type Stats struct {
	PeakTempC     float64
	SensorPresent bool
	Pauses        int
	Bursts        int
	TotalBurst    time.Duration
	TotalCooldown time.Duration
	DutyAchieved  float64
}

// Snapshot returns current statistics.
func (g *Governor) Snapshot() Stats {
	g.statsMu.Lock()
	defer g.statsMu.Unlock()
	var duty float64
	if total := g.totalBurst + g.totalCooldown; total > 0 {
		duty = float64(g.totalBurst) / float64(total)
	}
	return Stats{
		PeakTempC:     g.peakC,
		SensorPresent: g.sensorOK,
		Pauses:        g.pauses,
		Bursts:        g.bursts,
		TotalBurst:    g.totalBurst,
		TotalCooldown: g.totalCooldown,
		DutyAchieved:  duty,
	}
}

// Run executes fn as a paced inference burst: acquire the single-burst lock,
// gate on temperature, run, record, then cool down before releasing.
//
// Cooling happens inside the lock on purpose. Releasing early would let a
// queued caller start immediately and defeat the duty cycle.
func (g *Governor) Run(ctx context.Context, fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := g.gate(ctx); err != nil {
		return err
	}

	start := time.Now()
	err := fn()
	burst := time.Since(start)

	g.statsMu.Lock()
	g.totalBurst += burst
	g.bursts++
	g.statsMu.Unlock()

	g.observeTemp()
	g.cool(ctx, burst)
	return err
}

// gate blocks while the package is hotter than CeilingC.
func (g *Governor) gate(ctx context.Context) error {
	deadline := time.Now().Add(g.MaxWait)
	for {
		temp, ok := g.observeTemp()
		if !ok || temp < g.CeilingC {
			return nil
		}
		g.statsMu.Lock()
		g.pauses++
		g.statsMu.Unlock()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(g.Poll):
			}
			t, ok := g.observeTemp()
			if !ok || t <= g.ResumeC {
				return nil
			}
			if time.Now().After(deadline) {
				// Do not deadlock an operator's session on a sensor that never
				// falls. Proceed, but the recorded peak preserves the evidence.
				return nil
			}
		}
	}
}

// cool idles for the interval implied by the configured duty cycle.
//
//	idle = burst * (1 - duty) / duty
//
// so duty=0.7 idles for ~0.43x the burst length. Capped at 20s: a very long
// generation should not strand the operator for half a minute.
func (g *Governor) cool(ctx context.Context, burst time.Duration) {
	if g.Duty >= 1 || burst <= 0 {
		return
	}
	idle := time.Duration(float64(burst) * (1 - g.Duty) / g.Duty)
	const floor = 150 * time.Millisecond
	const ceil = 20 * time.Second
	if idle < floor {
		idle = floor
	}
	if idle > ceil {
		idle = ceil
	}

	select {
	case <-ctx.Done():
	case <-time.After(idle):
	}

	g.statsMu.Lock()
	g.totalCooldown += idle
	g.statsMu.Unlock()
}

// BatchPause is the inter-item pause for long agent loops (inbox batches,
// multi-member scans). It is deliberately larger than the per-burst cooldown:
// batch work is the realistic path to a sustained thermal event, and an offline
// queue has no latency requirement to protect.
func (g *Governor) BatchPause(ctx context.Context, index int) {
	if index <= 0 {
		return
	}
	d := 1200 * time.Millisecond
	if temp, ok := g.observeTemp(); ok {
		switch {
		case temp >= g.CeilingC:
			d = 8 * time.Second
		case temp >= g.ResumeC:
			d = 4 * time.Second
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
	g.statsMu.Lock()
	g.totalCooldown += d
	g.statsMu.Unlock()
}

// observeTemp reads the hottest CPU sensor and records the running peak.
func (g *Governor) observeTemp() (float64, bool) {
	temp, ok := ReadHottestC()
	g.statsMu.Lock()
	g.sensorChecked = true
	if ok {
		g.sensorOK = true
		if temp > g.peakC {
			g.peakC = temp
		}
	}
	g.statsMu.Unlock()
	return temp, ok
}

// ReadHottestC returns the hottest CPU-ish temperature in Celsius.
//
// Linux exposes millidegrees at /sys/class/thermal/thermal_zone*/temp. Zones
// that are clearly not CPU (battery, ambient, wifi) are skipped by type so a
// warm battery cannot trigger a false gate. Returns ok=false where no sensor is
// readable, which is the normal case in a container and on Windows.
func ReadHottestC() (float64, bool) {
	zones, err := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	if err != nil || len(zones) == 0 {
		return coretempFallback()
	}
	var best float64
	var found bool
	for _, z := range zones {
		if !cpuZone(filepath.Join(filepath.Dir(z), "type")) {
			continue
		}
		c, ok := readMilliC(z)
		if !ok {
			continue
		}
		found = true
		if c > best {
			best = c
		}
	}
	if !found {
		return coretempFallback()
	}
	return best, true
}

// cpuZone decides whether a thermal zone represents package/core silicon.
// Unknown types are accepted: a missed CPU zone is worse than an extra one,
// because the whole point is to gate before 85 °C.
func cpuZone(typePath string) bool {
	raw, err := os.ReadFile(typePath)
	if err != nil {
		return true
	}
	t := strings.ToLower(strings.TrimSpace(string(raw)))
	for _, bad := range []string{"battery", "charger", "ambient", "skin", "wifi", "wlan", "gpu", "disk", "nvme"} {
		if strings.Contains(t, bad) {
			return false
		}
	}
	return true
}

// coretempFallback reads hwmon, which is where Intel coretemp and AMD k10temp
// land on kernels that do not register a thermal zone.
func coretempFallback() (float64, bool) {
	inputs, err := filepath.Glob("/sys/class/hwmon/hwmon*/temp*_input")
	if err != nil || len(inputs) == 0 {
		return 0, false
	}
	var best float64
	var found bool
	for _, in := range inputs {
		name, _ := os.ReadFile(filepath.Join(filepath.Dir(in), "name"))
		n := strings.ToLower(strings.TrimSpace(string(name)))
		if n != "" && !strings.Contains(n, "coretemp") && !strings.Contains(n, "k10temp") &&
			!strings.Contains(n, "zenpower") && !strings.Contains(n, "acpitz") {
			continue
		}
		c, ok := readMilliC(in)
		if !ok {
			continue
		}
		found = true
		if c > best {
			best = c
		}
	}
	return best, found
}

func readMilliC(path string) (float64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil {
		return 0, false
	}
	c := v / 1000.0
	// Sanity window: rejects both raw-Celsius files and garbage readings.
	if c < 5 || c > 130 {
		return 0, false
	}
	return c, true
}
