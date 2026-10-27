// Package metrics samples container stats on a fixed cadence and keeps a
// rolling history for charts.
package metrics

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/krish-vd/berth/internal/docker"
)

// HistoryLen is how many samples each series keeps (1h at 1s cadence).
const HistoryLen = 3600

// Ring is a fixed-size float ring buffer.
type Ring struct {
	data []float64
	head int
	n    int
}

func NewRing(size int) *Ring { return &Ring{data: make([]float64, size)} }

func (r *Ring) Push(v float64) {
	r.data[r.head] = v
	r.head = (r.head + 1) % len(r.data)
	if r.n < len(r.data) {
		r.n++
	}
}

// Last returns up to n most recent values, oldest first.
func (r *Ring) Last(n int) []float64 {
	if n > r.n {
		n = r.n
	}
	out := make([]float64, n)
	start := (r.head - n + len(r.data)) % len(r.data)
	for i := 0; i < n; i++ {
		out[i] = r.data[(start+i)%len(r.data)]
	}
	return out
}

func (r *Ring) Len() int { return r.n }

// Metric identifies a series.
type Metric int

const (
	CPU Metric = iota
	Mem
	NetRx
	NetTx
	BlkRead
	BlkWrite
	numMetrics
)

// Current is the latest computed sample for one container.
type Current struct {
	CPU        float64 // percent of one core * cores (docker stats semantics)
	MemUsed    uint64
	MemLimit   uint64
	NetRx      float64 // bytes/s
	NetTx      float64
	BlkRead    float64
	BlkWrite   float64
	Pids       uint64
	SampledAt  time.Time
	HasSamples bool
}

func (c Current) MemPct() float64 {
	if c.MemLimit == 0 {
		return 0
	}
	return float64(c.MemUsed) / float64(c.MemLimit) * 100
}

type raw struct {
	at              time.Time
	cpuTotal, cpuSy uint64
	rx, tx, br, bw  uint64
}

type series struct {
	rings [numMetrics]*Ring
	cur   Current
	prev  *raw
}

func newSeries() *series {
	s := &series{}
	for i := range s.rings {
		s.rings[i] = NewRing(HistoryLen)
	}
	return s
}

// Snapshot is what the UI receives after each sampling round.
type Snapshot struct {
	Containers []docker.Container
	Info       docker.Info
	Err        error
	At         time.Time
}

// Sampler owns all history. Safe for concurrent readers.
type Sampler struct {
	c        *docker.Client
	interval time.Duration

	mu     sync.RWMutex
	series map[string]*series
	host   [numMetrics]*Ring
	info   docker.Info
}

func NewSampler(c *docker.Client, interval time.Duration) *Sampler {
	s := &Sampler{c: c, interval: interval, series: map[string]*series{}}
	for i := range s.host {
		s.host[i] = NewRing(HistoryLen)
	}
	return s
}

// Run samples until ctx is done, calling emit after every round.
func (s *Sampler) Run(ctx context.Context, emit func(Snapshot)) {
	if info, err := s.c.Info(ctx); err == nil {
		s.mu.Lock()
		s.info = info
		s.mu.Unlock()
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		emit(s.round(ctx))
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Sampler) round(ctx context.Context) Snapshot {
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second) // a busy host can take seconds to list
	defer cancel()

	cs, err := s.c.Containers(rctx)
	if err != nil {
		return Snapshot{Err: err, At: time.Now()}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name() < cs[j].Name() })

	type result struct {
		id string
		st *docker.Stats
	}
	results := make(chan result, len(cs))
	sem := make(chan struct{}, max(1, s.c.Parallel))
	var wg sync.WaitGroup
	for _, c := range cs {
		if c.State != "running" {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sctx, scancel := context.WithTimeout(rctx, 5*time.Second) // one slow container must not stall the round
			st, err := s.c.Stats(sctx, id)
			scancel()
			if err == nil {
				results <- result{id, st}
			}
		}(c.ID)
	}
	wg.Wait()
	close(results)

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	seen := map[string]bool{}
	var hostCPU, hostMem, hRx, hTx, hBr, hBw float64
	for r := range results {
		seen[r.id] = true
		se := s.series[r.id]
		if se == nil {
			se = newSeries()
			s.series[r.id] = se
		}
		st := r.st
		rx, tx := st.NetIO()
		br, bw := st.BlockIO()
		cur := &raw{at: now, cpuTotal: st.CPUStats.CPUUsage.TotalUsage, cpuSy: st.CPUStats.SystemUsage, rx: rx, tx: tx, br: br, bw: bw}

		c := Current{MemUsed: st.MemUsed(), MemLimit: st.MemoryStats.Limit, Pids: st.PidsStats.Current, SampledAt: now}
		if p := se.prev; p != nil {
			dt := now.Sub(p.at).Seconds()
			cpus := float64(st.CPUStats.OnlineCPUs)
			if cpus == 0 {
				cpus = float64(s.info.NCPU)
			}
			if sys := delta(cur.cpuSy, p.cpuSy); sys > 0 {
				c.CPU = float64(delta(cur.cpuTotal, p.cpuTotal)) / float64(sys) * cpus * 100
			}
			if dt > 0 {
				c.NetRx = float64(delta(rx, p.rx)) / dt
				c.NetTx = float64(delta(tx, p.tx)) / dt
				c.BlkRead = float64(delta(br, p.br)) / dt
				c.BlkWrite = float64(delta(bw, p.bw)) / dt
			}
			c.HasSamples = true
			se.rings[CPU].Push(c.CPU)
			se.rings[Mem].Push(float64(c.MemUsed))
			se.rings[NetRx].Push(c.NetRx)
			se.rings[NetTx].Push(c.NetTx)
			se.rings[BlkRead].Push(c.BlkRead)
			se.rings[BlkWrite].Push(c.BlkWrite)
		}
		se.prev = cur
		se.cur = c

		hostCPU += c.CPU
		hostMem += float64(c.MemUsed)
		hRx += c.NetRx
		hTx += c.NetTx
		hBr += c.BlkRead
		hBw += c.BlkWrite
	}
	// Stopped containers: drop rate state but keep history.
	for id, se := range s.series {
		if !seen[id] {
			se.prev = nil
			se.cur = Current{}
		}
	}
	if n := float64(s.info.NCPU); n > 0 {
		hostCPU /= n
	}
	for i, v := range []float64{hostCPU, hostMem, hRx, hTx, hBr, hBw} {
		s.host[i].Push(v)
	}
	return Snapshot{Containers: cs, Info: s.info, At: now}
}

func delta(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// Current returns the latest sample for a container.
func (s *Sampler) Current(id string) Current {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if se := s.series[id]; se != nil {
		return se.cur
	}
	return Current{}
}

// History returns up to n most recent values of m for container id.
func (s *Sampler) History(id string, m Metric, n int) []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if se := s.series[id]; se != nil {
		return se.rings[m].Last(n)
	}
	return nil
}

// HostHistory returns aggregate history across all containers.
// For CPU the value is percent of total host capacity.
func (s *Sampler) HostHistory(m Metric, n int) []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.host[m].Last(n)
}
