package hostmetrics

import (
	"testing"
	"time"
)

// TestSnapshotLive exercises the real /proc reads + statfs and the rate path.
func TestSnapshotLive(t *testing.T) {
	c := New()
	if _, err := c.Snapshot(); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	s, err := c.Snapshot()
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if s.MemTotal <= 0 || s.DiskTotal <= 0 {
		t.Fatalf("memTotal=%d diskTotal=%d; want > 0", s.MemTotal, s.DiskTotal)
	}
	if s.CPUPercent < 0 || s.CPUPercent > 100 {
		t.Fatalf("cpu=%.2f; want 0..100", s.CPUPercent)
	}
	if s.MemUsed < 0 || s.MemUsed > s.MemTotal {
		t.Fatalf("memUsed=%d out of range (total %d)", s.MemUsed, s.MemTotal)
	}
}

func TestParseCPU(t *testing.T) {
	// idle(3rd)=100, iowait(4th)=10 => idle=110; total=10+20+30+100+10=170.
	b := []byte("cpu  10 20 30 100 10 0 0 0 0 0\ncpu0 5 10 15 50 5 0 0 0 0 0\n")
	idle, total, err := parseCPU(b)
	if err != nil {
		t.Fatalf("parseCPU: %v", err)
	}
	if idle != 110 || total != 170 {
		t.Fatalf("idle=%d total=%d; want 110/170", idle, total)
	}
}

func TestCPUPercent(t *testing.T) {
	// Between samples: totalΔ=100, idleΔ=25 => 75% busy.
	prev := raw{cpuIdle: 110, cpuTotal: 170}
	cur := raw{cpuIdle: 135, cpuTotal: 270}
	if p := cpuPercent(prev, cur); p < 74.9 || p > 75.1 {
		t.Fatalf("cpuPercent = %.2f; want ~75", p)
	}
	// Counter reset => 0, never negative.
	if p := cpuPercent(raw{cpuTotal: 500}, raw{cpuTotal: 100}); p != 0 {
		t.Fatalf("reset cpuPercent = %.2f; want 0", p)
	}
}

func TestParseMem(t *testing.T) {
	b := []byte("MemTotal:       16384 kB\nMemFree:         2048 kB\nMemAvailable:    4096 kB\n")
	used, total, err := parseMem(b)
	if err != nil {
		t.Fatalf("parseMem: %v", err)
	}
	if total != 16384*1024 || used != (16384-4096)*1024 {
		t.Fatalf("used=%d total=%d; want %d/%d", used, total, (16384-4096)*1024, 16384*1024)
	}
}

func TestParseNetDev(t *testing.T) {
	// lo and docker0 are excluded; only ens18 counts.
	b := []byte(
		"Inter-|   Receive                    |  Transmit\n" +
			" face |bytes packets errs ... \n" +
			"    lo: 999 1 0 0 0 0 0 0 999 1 0 0 0 0 0 0\n" +
			" ens18: 1000 5 0 0 0 0 0 0 2000 6 0 0 0 0 0 0\n" +
			"docker0: 500 1 0 0 0 0 0 0 700 1 0 0 0 0 0 0\n")
	rx, tx := parseNetDev(b)
	if rx != 1000 || tx != 2000 {
		t.Fatalf("rx=%d tx=%d; want 1000/2000 (only ens18)", rx, tx)
	}
}

func TestParseDiskstats(t *testing.T) {
	// sda counts; sda1 (partition) and loop0 are excluded. Fields:
	// major minor name reads rmerged sectorsRead msRead writes wmerged sectorsWritten ...
	b := []byte(
		"   8       0 sda 100 0 200 0 50 0 80 0 0 0 0\n" +
			"   8       1 sda1 10 0 20 0 5 0 8 0 0 0 0\n" +
			"   7       0 loop0 1 0 2 0 1 0 1 0 0 0 0\n")
	rd, wr := parseDiskstats(b)
	if rd != 200*sectorSize || wr != 80*sectorSize {
		t.Fatalf("rd=%d wr=%d; want %d/%d (only sda)", rd, wr, 200*sectorSize, 80*sectorSize)
	}
}

func TestIsPhysicalDisk(t *testing.T) {
	cases := map[string]bool{
		"sda": true, "sda1": false, "vda": true, "nvme0n1": true,
		"nvme0n1p1": false, "mmcblk0": true, "loop0": false, "dm-0": false, "sr0": false,
	}
	for name, want := range cases {
		if got := isPhysicalDisk(name); got != want {
			t.Errorf("isPhysicalDisk(%q)=%v; want %v", name, got, want)
		}
	}
}
