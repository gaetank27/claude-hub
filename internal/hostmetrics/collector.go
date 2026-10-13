// Package hostmetrics collects physical-host metrics (CPU, RAM, network and disk
// I/O, disk space) by reading /proc and statfs — no external dependency, no cgo.
// Rates (network, disk I/O) are derived from the delta between consecutive
// samples, so the first Snapshot after start reports zero rates.
package hostmetrics

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// File paths read by the collector (package-level so tests can stub disk space).
const (
	procStat      = "/proc/stat"
	procMeminfo   = "/proc/meminfo"
	procNetDev    = "/proc/net/dev"
	procDiskstats = "/proc/diskstats"
	rootMount     = "/"
)

const sectorSize = 512 // /proc/diskstats reports I/O in 512-byte sectors

// Sample is an instantaneous host-metrics reading. Counters are resolved to
// per-second rates; memory and disk space are absolute byte values.
type Sample struct {
	CPUPercent float64   `json:"cpu"`        // 0..100, global
	MemUsed    int64     `json:"memUsed"`    // bytes (total - available)
	MemTotal   int64     `json:"memTotal"`   // bytes
	NetRx      float64   `json:"netRx"`      // bytes/s received
	NetTx      float64   `json:"netTx"`      // bytes/s transmitted
	DiskRd     float64   `json:"diskRd"`     // bytes/s read
	DiskWr     float64   `json:"diskWr"`     // bytes/s written
	DiskUsed   int64     `json:"diskUsed"`   // bytes used on /
	DiskTotal  int64     `json:"diskTotal"`  // bytes total on /
	SampledAt  time.Time `json:"sampledAt"`
}

// raw holds the cumulative counters needed to compute rates between samples.
type raw struct {
	t                  time.Time
	cpuIdle, cpuTotal  uint64
	netRx, netTx       uint64
	diskRd, diskWr     uint64 // bytes
}

// Collector reads the host metrics, remembering the previous raw counters to
// derive rates. Safe for concurrent use.
type Collector struct {
	mu      sync.Mutex
	prev    raw
	hasPrev bool
}

// New returns a ready collector.
func New() *Collector { return &Collector{} }

// Snapshot reads the current metrics and computes rates against the previous
// reading. The first call (no previous sample) returns zero rates.
func (c *Collector) Snapshot() (Sample, error) {
	now := time.Now()

	statB, err := os.ReadFile(procStat)
	if err != nil {
		return Sample{}, err
	}
	idle, total, err := parseCPU(statB)
	if err != nil {
		return Sample{}, err
	}
	memB, err := os.ReadFile(procMeminfo)
	if err != nil {
		return Sample{}, err
	}
	memUsed, memTotal, err := parseMem(memB)
	if err != nil {
		return Sample{}, err
	}
	netB, err := os.ReadFile(procNetDev)
	if err != nil {
		return Sample{}, err
	}
	netRx, netTx := parseNetDev(netB)
	diskB, err := os.ReadFile(procDiskstats)
	if err != nil {
		return Sample{}, err
	}
	diskRd, diskWr := parseDiskstats(diskB)
	diskUsed, diskTotal := diskSpace(rootMount)

	cur := raw{t: now, cpuIdle: idle, cpuTotal: total, netRx: netRx, netTx: netTx, diskRd: diskRd, diskWr: diskWr}

	s := Sample{
		MemUsed:   memUsed,
		MemTotal:  memTotal,
		DiskUsed:  diskUsed,
		DiskTotal: diskTotal,
		SampledAt: now,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasPrev {
		s.CPUPercent = cpuPercent(c.prev, cur)
		dt := now.Sub(c.prev.t).Seconds()
		if dt > 0 {
			s.NetRx = perSec(c.prev.netRx, cur.netRx, dt)
			s.NetTx = perSec(c.prev.netTx, cur.netTx, dt)
			s.DiskRd = perSec(c.prev.diskRd, cur.diskRd, dt)
			s.DiskWr = perSec(c.prev.diskWr, cur.diskWr, dt)
		}
	}
	c.prev = cur
	c.hasPrev = true
	return s, nil
}

// perSec returns (cur-prev)/dt, clamped to 0 on a counter reset (reboot/wrap).
func perSec(prev, cur uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return float64(cur-prev) / dt
}

// cpuPercent derives global CPU utilisation from the idle/total deltas.
func cpuPercent(prev, cur raw) float64 {
	dt := cur.cpuTotal - prev.cpuTotal
	di := cur.cpuIdle - prev.cpuIdle
	if cur.cpuTotal < prev.cpuTotal || dt == 0 {
		return 0
	}
	p := (1 - float64(di)/float64(dt)) * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// parseCPU reads the aggregate "cpu" line of /proc/stat. idle = idle + iowait;
// total = sum of all columns.
func parseCPU(b []byte) (idle, total uint64, err error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		for i, v := range f[1:] {
			n, e := strconv.ParseUint(v, 10, 64)
			if e != nil {
				continue
			}
			total += n
			if i == 3 || i == 4 { // idle, iowait
				idle += n
			}
		}
		return idle, total, nil
	}
	return 0, 0, fmt.Errorf("hostmetrics: no cpu line in /proc/stat")
}

// parseMem reads MemTotal and MemAvailable from /proc/meminfo (kB → bytes); used
// is total - available.
func parseMem(b []byte) (used, total int64, err error) {
	var totalKB, availKB int64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			totalKB, _ = strconv.ParseInt(f[1], 10, 64)
			haveTotal = true
		case "MemAvailable:":
			availKB, _ = strconv.ParseInt(f[1], 10, 64)
			haveAvail = true
		}
		if haveTotal && haveAvail {
			break
		}
	}
	if !haveTotal {
		return 0, 0, fmt.Errorf("hostmetrics: MemTotal not found")
	}
	return (totalKB - availKB) * 1024, totalKB * 1024, nil
}

// virtualIface matches network interfaces that are not physical NICs, so their
// traffic is excluded from the host totals.
var virtualIface = regexp.MustCompile(`^(lo|docker|veth|br-|virbr|vnet|tap|tun|vmnet|cni|flannel|kube|wg)`)

// parseNetDev sums received/transmitted bytes across physical interfaces.
func parseNetDev(b []byte) (rx, tx uint64) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue // header lines
		}
		name := strings.TrimSpace(line[:i])
		if virtualIface.MatchString(name) {
			continue
		}
		f := strings.Fields(line[i+1:])
		if len(f) < 9 {
			continue
		}
		if v, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			rx += v // rx bytes
		}
		if v, err := strconv.ParseUint(f[8], 10, 64); err == nil {
			tx += v // tx bytes
		}
	}
	return rx, tx
}

var (
	reSCSI = regexp.MustCompile(`^(sd|vd|hd|xvd)[a-z]+$`) // whole disk, not a partition
	reNVMe = regexp.MustCompile(`^nvme\d+n\d+$`)
	reMMC  = regexp.MustCompile(`^mmcblk\d+$`)
)

// isPhysicalDisk reports whether a /proc/diskstats device is a whole physical
// disk (so partitions, loop/ram/dm devices are not double-counted).
func isPhysicalDisk(name string) bool {
	return reSCSI.MatchString(name) || reNVMe.MatchString(name) || reMMC.MatchString(name)
}

// parseDiskstats sums bytes read/written across physical disks (sectors × 512).
func parseDiskstats(b []byte) (rd, wr uint64) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		if !isPhysicalDisk(f[2]) {
			continue
		}
		if v, err := strconv.ParseUint(f[5], 10, 64); err == nil {
			rd += v * sectorSize // sectors read
		}
		if v, err := strconv.ParseUint(f[9], 10, 64); err == nil {
			wr += v * sectorSize // sectors written
		}
	}
	return rd, wr
}

// diskSpace returns used/total bytes for the filesystem at path (0,0 on error).
func diskSpace(path string) (used, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := int64(st.Bsize)
	total = int64(st.Blocks) * bs
	used = int64(st.Blocks-st.Bfree) * bs
	return used, total
}
