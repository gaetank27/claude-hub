// Package claudemetrics historises Claude usage (tokens, cost, active sessions)
// per subscription account into fixed-size RRD files via the rrdtool CLI (no
// cgo), one file per account. A "Tous" view is produced by summing the per-account
// series on read. Cumulative counters (tokens, cost) are stored as DERIVE so
// rrdtool yields rates; Export converts those rates back to per-bucket totals.
package claudemetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// dsNames is the fixed data-source order; Update emits values in this order.
var dsNames = []string{"tok_in", "tok_out", "cost", "sess_active"}

// rateDS marks the DERIVE sources whose AVERAGE (a per-second rate) Export
// converts back into a per-bucket total (rate × resolution).
var rateDS = map[string]bool{"tok_in": true, "tok_out": true, "cost": true}

// costScale stores cost as integer micro-USD because DERIVE only accepts
// integers; Export divides the cost total back down to USD.
const costScale = 1e6

// Sample is the cumulative usage of one account at sampling time.
type Sample struct {
	TokIn      int64
	TokOut     int64
	Cost       float64
	SessActive int
}

// Store manages the per-account Claude RRD files.
type Store struct {
	dir  string
	bin  string
	step int
}

// NewStore returns a store writing under dir (one <accountID>.rrd per account).
func NewStore(dir, bin string, step int) *Store {
	if bin == "" {
		bin = "rrdtool"
	}
	if step <= 0 {
		step = 30
	}
	return &Store{dir: dir, bin: bin, step: step}
}

// Available reports whether the rrdtool binary is resolvable.
func (s *Store) Available() bool {
	_, err := exec.LookPath(s.bin)
	return err == nil
}

var unsafePath = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func (s *Store) pathFor(accountID string) string {
	return filepath.Join(s.dir, unsafePath.ReplaceAllString(accountID, "_")+".rrd")
}

// EnsureCreated creates the account's RRD if missing: DERIVE token/cost counters,
// a GAUGE active-session count, and AVERAGE RRAs at 1h/24h/7d/31d (bounded file).
func (s *Store) EnsureCreated(ctx context.Context, accountID string) error {
	path := s.pathFor(accountID)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	hb := strconv.Itoa(s.step * 3)
	args := []string{"create", path, "--step", strconv.Itoa(s.step)}
	for _, ds := range []string{"tok_in", "tok_out", "cost"} {
		args = append(args, "DS:"+ds+":DERIVE:"+hb+":0:U") // cumulative → rate; min 0 drops resets
	}
	args = append(args, "DS:sess_active:GAUGE:"+hb+":0:U")
	stepsPerRow := map[int]int{1: 120, 10: 288, 60: 336, 240: 372}
	for _, pdp := range []int{1, 10, 60, 240} {
		args = append(args, fmt.Sprintf("RRA:AVERAGE:0.5:%d:%d", pdp, stepsPerRow[pdp]))
	}
	return s.run(ctx, args...)
}

// Update appends one cumulative sample for an account (creating the file first).
func (s *Store) Update(ctx context.Context, accountID string, m Sample) error {
	if err := s.EnsureCreated(ctx, accountID); err != nil {
		return err
	}
	vals := []string{
		strconv.FormatInt(m.TokIn, 10),
		strconv.FormatInt(m.TokOut, 10),
		strconv.FormatInt(int64(m.Cost*costScale+0.5), 10), // micro-USD (DERIVE needs an integer)
		strconv.Itoa(m.SessActive),
	}
	return s.run(ctx, "update", s.pathFor(accountID), "N:"+strings.Join(vals, ":"))
}

// Point is one (timestamp, value) of a series; V is nil for an UNKNOWN bucket.
type Point struct {
	T int64    `json:"t"`
	V *float64 `json:"v"`
}

// History is the exported, account-scoped (or aggregated) series for a range.
type History struct {
	Available bool               `json:"available"`
	Range     string             `json:"range"`
	From      int64              `json:"from"`
	To        int64              `json:"to"`
	Step      int64              `json:"step"`
	Series    map[string][]Point `json:"series"`
}

var rangeSpec = map[string]struct {
	start string
	res   int
}{
	"hour":  {"-1h", 30},
	"day":   {"-24h", 300},
	"week":  {"-7d", 1800},
	"month": {"-31d", 7200},
}

// Export returns one account's series for a range. DERIVE rates are converted to
// per-bucket totals (rate × resolution). A missing file yields an error.
func (s *Store) Export(ctx context.Context, accountID, rng string) (History, error) {
	spec, ok := rangeSpec[rng]
	if !ok {
		return History{}, fmt.Errorf("unknown range %q", rng)
	}
	path := s.pathFor(accountID)
	args := []string{"xport", "--json", "--start", spec.start, "--end", "now", "--step", strconv.Itoa(spec.res)}
	for _, ds := range dsNames {
		args = append(args, fmt.Sprintf("DEF:%s=%s:%s:AVERAGE", ds, path, ds))
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
		t := x.Meta.Start + x.Meta.Step*int64(i+1)
		for j, name := range x.Meta.Legend {
			if j >= len(row) {
				continue
			}
			v := row[j]
			if v != nil && rateDS[name] {
				total := *v * float64(x.Meta.Step) // rate/s → total over the bucket
				if name == "cost" {
					total /= costScale // micro-USD → USD
				}
				v = &total
			}
			h.Series[name] = append(h.Series[name], Point{T: t, V: v})
		}
	}
	return h, nil
}

// ExportAggregate sums several accounts' series ("Tous"). Files that can't be read
// (e.g. an account with no samples yet) are skipped.
func (s *Store) ExportAggregate(ctx context.Context, accountIDs []string, rng string) (History, error) {
	var hists []History
	for _, id := range accountIDs {
		h, err := s.Export(ctx, id, rng)
		if err != nil {
			continue // no data for this account yet
		}
		hists = append(hists, h)
	}
	if len(hists) == 0 {
		return History{Available: true, Range: rng, Series: map[string][]Point{}}, nil
	}
	return Aggregate(rng, hists), nil
}

// Aggregate sums per-account histories point-by-point (timelines align: same range
// ⇒ same start/step/length). A bucket is null only when null in every history.
func Aggregate(rng string, hists []History) History {
	base := hists[0]
	out := History{Available: true, Range: rng, From: base.From, To: base.To, Step: base.Step, Series: map[string][]Point{}}
	for _, name := range dsNames {
		var pts []Point
		for i := range base.Series[name] {
			p := Point{T: base.Series[name][i].T}
			var sum float64
			any := false
			for _, h := range hists {
				s := h.Series[name]
				if i < len(s) && s[i].V != nil {
					sum += *s[i].V
					any = true
				}
			}
			if any {
				v := sum
				p.V = &v
			}
			pts = append(pts, p)
		}
		out.Series[name] = pts
	}
	return out
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
