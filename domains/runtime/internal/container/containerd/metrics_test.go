package containerd

import (
	"testing"
	"time"

	cgroup1stats "github.com/containerd/cgroups/v3/cgroup1/stats"
	cgroup2stats "github.com/containerd/cgroups/v3/cgroup2/stats"
)

func TestBuildCPUMetricsUsesCumulativeDelta(t *testing.T) {
	start := time.Unix(0, 0)
	first := taskMetricsSample{
		timestamp:   start,
		cpuUsageNS:  100_000_000,
		cpuUserNS:   60_000_000,
		cpuKernelNS: 40_000_000,
	}
	second := taskMetricsSample{
		timestamp:   start.Add(200 * time.Millisecond),
		cpuUsageNS:  200_000_000,
		cpuUserNS:   120_000_000,
		cpuKernelNS: 80_000_000,
	}

	metrics := buildCPUMetrics(first, second)
	if metrics == nil {
		t.Fatal("expected cpu metrics")
		return
	}
	if metrics.UsagePercent != 50 {
		t.Fatalf("expected cpu usage percent 50, got %v", metrics.UsagePercent)
	}
	if metrics.UsageNanoseconds != second.cpuUsageNS {
		t.Fatalf("expected latest cpu usage %d, got %d", second.cpuUsageNS, metrics.UsageNanoseconds)
	}
}

func TestSampleFromCgroup1(t *testing.T) {
	sample := sampleFromCgroup1(time.Unix(1, 0), &cgroup1stats.Metrics{
		CPU: &cgroup1stats.CPUStat{
			Usage: &cgroup1stats.CPUUsage{
				Total:  12,
				User:   7,
				Kernel: 5,
			},
		},
		Memory: &cgroup1stats.MemoryStat{
			Usage: &cgroup1stats.MemoryEntry{
				Usage: 4096,
				Limit: 8192,
			},
		},
	})

	if sample.cpuUsageNS != 12 || sample.cpuUserNS != 7 || sample.cpuKernelNS != 5 {
		t.Fatalf("unexpected cpu sample: %+v", sample)
	}
	if sample.memoryUsage != 4096 || sample.memoryLimit != 8192 {
		t.Fatalf("unexpected memory sample: %+v", sample)
	}
}

func TestSampleFromCgroup2(t *testing.T) {
	sample := sampleFromCgroup2(time.Unix(2, 0), &cgroup2stats.Metrics{
		CPU: &cgroup2stats.CPUStat{
			UsageUsec:  12,
			UserUsec:   7,
			SystemUsec: 5,
		},
		Memory: &cgroup2stats.MemoryStat{
			Usage:      16_384,
			UsageLimit: 32_768,
		},
	})

	if sample.cpuUsageNS != 12_000 || sample.cpuUserNS != 7_000 || sample.cpuKernelNS != 5_000 {
		t.Fatalf("unexpected cpu sample: %+v", sample)
	}
	if sample.memoryUsage != 16_384 || sample.memoryLimit != 32_768 {
		t.Fatalf("unexpected memory sample: %+v", sample)
	}
}

func TestNormalizeMemoryLimitTreatsHugeValueAsUnlimited(t *testing.T) {
	if got := normalizeMemoryLimit(maxPracticalMemoryLimitBytes + 1); got != 0 {
		t.Fatalf("expected unlimited memory limit to normalize to 0, got %d", got)
	}
}
