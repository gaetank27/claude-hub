package hostmetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// dsNames is the fixed data-source order. Update() emits values in this order and
// Export() requests them by name, so the two stay in sync.
var dsNames = []string{
	"cpu", "mem_used", "mem_total", "net_rx", "net_tx",
	"disk_rd", "disk_wr", "disk_used", "disk_total",
}

// Store manages the fixed-size RRD database for host metrics by shelling out to
// the rrdtool CLI (no cgo). The round-robin archives cap the file size: it never
// grows past creation, so the disk can't be saturated by history.
type Store struct {
	path string // .rrd file
	bin  string // rrdtool binary (name or absolute path)
	step int    // base step in seconds
}

// NewStore returns a store for the given .rrd path and rrdtool binary.
func NewStore(path, bin string, step int) *Store {
	if bin == "" {
		bin = "rrdtool"
	}
	if step <= 0 {
		step = 30
	}
	return &Store{path: path, bin: bin, step: step}
}

// Available reports whether the rrdtool binary is resolvable.
func (s *Store) Available() bool {
	_, err := exec.LookPath(s.bin)
	return err == nil
}

// EnsureCreated creates the RRD with the metric data sources and the multi-scale
// round-robin archives if the file does not exist yet. RRAs (AVERAGE): 1h@step,
// 24h@5min, 7d@30min, ~31d@2h — bounded storage across the four chart ranges.
func (s *Store) EnsureCreated(ctx context.Context) error {
	if _, err := os.Stat(s.path); err == nil {
		return nil // already created
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	hb := strconv.Itoa(s.step * 3) // heartbeat: tolerate two missed samples
	args := []string{"create", s.path, "--step", strconv.Itoa(s.step)}
	// cpu is a percentage (0..100); everything else is unbounded (0..U).
	args = append(args, "DS:cpu:GAUGE:"+hb+":0:100")
	for _, ds := range dsNames[1:] {
		args = append(args, "DS:"+ds+":GAUGE:"+hb+":0:U")
	}
	stepsPerRow := map[int]int{1: 120, 10: 288, 60: 336, 240: 372}
	for _, pdp := range []int{1, 10, 60, 240} {
		args = append(args, fmt.Sprintf("RRA:AVERAGE:0.5:%d:%d", pdp, stepsPerRow[pdp]))
	}
	return s.run(ctx, args...)
}

// Update appends one sample at the current time. DS order matches dsNames.
func (s *Store) Update(ctx context.Context, m Sample) error {
	vals := []string{
		strconv.FormatFloat(m.CPUPercent, 'f', 2, 64),
		strconv.FormatInt(m.MemUsed, 10),
		strconv.FormatInt(m.MemTotal, 10),
		strconv.FormatFloat(m.NetRx, 'f', 0, 64),
		strconv.FormatFloat(m.NetTx, 'f', 0, 64),
		strconv.FormatFloat(m.DiskRd, 'f', 0, 64),
		strconv.FormatFloat(m.DiskWr, 'f', 0, 64),
		strconv.FormatInt(m.DiskUsed, 10),
		strconv.FormatInt(m.DiskTotal, 10),
	}
	return s.run(ctx, "update", s.path, "N:"+strings.Join(vals, ":"))
}

// Point is one (timestamp, value) of a series; V is nil for an UNKNOWN bucket.
type Point struct {
	T int64    `json:"t"` // unix seconds
	V *float64 `json:"v"`
}

// History is the exported series for a chart range.
type History struct {
	Available bool               `json:"available"`
	Range     string             `json:"range"`
	From      int64              `json:"from"`
	To        int64              `json:"to"`
	Step      int64              `json:"step"`
	Series    map[string][]Point `json:"series"`
}

// rangeSpec maps a UI range to (rrdtool start, export resolution in seconds),
// matching the RRA granularities so rrdtool serves pre-consolidated data.
var rangeSpec = map[string]struct {
	start string
	res   int
}{
	"hour":  {"-1h", 30},
	"day":   {"-24h", 300},
	"week":  {"-7d", 1800},
	"month": {"-31d", 7200},
}

// Export runs `rrdtool xport` for the given range and returns per-DS series.
func (s *Store) Export(ctx context.Context, rng string) (History, error) {
	spec, ok := rangeSpec[rng]
	if !ok {
		return History{}, fmt.Errorf("unknown range %q", rng)
	}
	args := []string{"xport", "--json", "--start", spec.start, "--end", "now", "--step", strconv.Itoa(spec.res)}
	for _, ds := range dsNames {
		args = append(args, fmt.Sprintf("DEF:%s=%s:%s:AVERAGE", ds, s.path, ds))
		args = append(args, fmt.Sprintf("XPORT:%s:%s", ds, ds))
	}
	out, err := s.output(ctx, args...)
	if err != nil {
		return History{}, err
	}

	var x struct {
		Meta struct {
			Start  int64    `json:"start"`
			Step   int64    `json:"step"`
			End    int64    `json:"end"`
			Legend []string `json:"legend"`
		} `json:"meta"`
		Data [][]*float64 `json:"data"`
	}
	if err := json.Unmarshal(out, &x); err != nil {
		return History{}, fmt.Errorf("parse xport: %w", err)
	}

	h := History{
		Available: true, Range: rng,
		From: x.Meta.Start, To: x.Meta.End, Step: x.Meta.Step,
		Series: make(map[string][]Point, len(x.Meta.Legend)),
	}
	for i, row := range x.Data {
		// rrdtool's first data row is at start+step.
		t := x.Meta.Start + x.Meta.Step*int64(i+1)
		for j, name := range x.Meta.Legend {
			if j >= len(row) {
				continue
			}
			h.Series[name] = append(h.Series[name], Point{T: t, V: row[j]})
		}
	}
	return h, nil
}

func (s *Store) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, s.bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rrdtool %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Store) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, s.bin, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rrdtool %s: %w", args[0], err)
	}
	return out, nil
}
