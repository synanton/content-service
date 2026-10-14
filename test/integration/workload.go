//go:build integration

package integration

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"sync"
)

// Workload describes a reproducible load scenario. Identity is
// generator-version + seed + config: the same identity replays the same
// size/tenant/dedup distributions (op order is deterministic only at
// parallelism 1; higher parallelism fixes the distributions, not the
// interleaving — the honest caveat for any parallel load generator).
type Workload struct {
	Name         string
	Seed         int64
	Tenants      int
	Bands        []SizeBand
	DedupRatio   float64
	ReadFraction float64
}

// SizeBand is one payload-size class with its selection weight.
type SizeBand struct {
	Name   string
	Size   int
	Weight int
}

// MixedWorkload is the default scenario: mostly small payloads with a
// long tail, some cross-tenant duplicates, half reads.
func MixedWorkload(seed int64) Workload {
	return Workload{
		Name:   "mixed",
		Seed:   seed,
		Tenants: 4,
		Bands: []SizeBand{
			{Name: "small", Size: 100, Weight: 50},
			{Name: "medium", Size: 5000, Weight: 30},
			{Name: "large", Size: 300 * 1024, Weight: 15},
			{Name: "xlarge", Size: 1024 * 1024, Weight: 5},
		},
		DedupRatio:   0.1,
		ReadFraction: 0.5,
	}
}

// Generator produces deterministic bytes and tenant assignments from the
// workload seed. Payload bytes derive from (seed, band, salt) so any op
// can be regenerated without storing history; dedup reuses earlier salts.
type Generator struct {
	rng     *rand.Rand
	work    Workload
	totalW  int
	tenants []string
}

// NewGenerator builds a deterministic generator for a workload.
func NewGenerator(work Workload) *Generator {
	tenants := make([]string, work.Tenants)
	for i := range tenants {
		tenants[i] = fmt.Sprintf("load-%s-%d", work.Name, i)
	}
	total := 0
	for _, b := range work.Bands {
		total += b.Weight
	}
	return &Generator{rng: rand.New(rand.NewSource(work.Seed)), work: work, totalW: total, tenants: tenants}
}

// Identity is the replay key for a workload configuration.
func (g *Generator) Identity() string {
	return fmt.Sprintf("workload-v1/seed=%d/tenants=%d/dedup=%.2f/read=%.2f",
		g.work.Seed, g.work.Tenants, g.work.DedupRatio, g.work.ReadFraction)
}

// Tenant picks a tenant uniformly at random.
func (g *Generator) Tenant() string {
	return g.tenants[g.rng.Intn(len(g.tenants))]
}

// Band picks a size band by weight.
func (g *Generator) Band() SizeBand {
	r := g.rng.Intn(g.totalW)
	for _, b := range g.work.Bands {
		r -= b.Weight
		if r < 0 {
			return b
		}
	}
	return g.work.Bands[len(g.work.Bands)-1]
}

// Payload renders deterministic bytes for a band and salt. Same inputs
// always yield the same bytes (dedup replays); different salts differ.
func Payload(seed int64, band SizeBand, salt uint64) []byte {
	payload := make([]byte, band.Size)
	base := uint64(seed) ^ uint64(len(band.Name))<<32
	for i := range payload {
		payload[i] = byte(base>>((i % 8) * 8)) + byte(i%251)
	}
	if len(payload) >= 8 {
		binary.BigEndian.PutUint64(payload, salt)
	}
	return payload
}

// Registry tracks published artifacts for reads and dedup replays.
// Bounded: per-tenant artifact lists ring at cap entries, payload pool
// holds the most recent payloads only.
type Registry struct {
	mu       sync.Mutex
	byTenant map[string][]string
	payloads [][]byte
	cap      int
}

// NewRegistry creates an empty registry holding at most cap entries per tenant.
func NewRegistry(cap int) *Registry {
	return &Registry{byTenant: map[string][]string{}, cap: cap}
}

// Record remembers a published artifact and its payload (for dedup replays).
func (r *Registry) Record(tenant, artifactID string, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := append(r.byTenant[tenant], artifactID)
	if len(list) > r.cap {
		list = list[len(list)-r.cap:]
	}
	r.byTenant[tenant] = list
	r.payloads = append(r.payloads, payload)
	if len(r.payloads) > r.cap {
		r.payloads = r.payloads[len(r.payloads)-r.cap:]
	}
}

// SampleArtifact returns a random known artifact of a tenant, or false.
func (r *Registry) SampleArtifact(tenant string, rng *rand.Rand) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.byTenant[tenant]
	if len(list) == 0 {
		return "", false
	}
	return list[rng.Intn(len(list))], true
}

// SamplePayload returns a recent payload for dedup replays, or false.
func (r *Registry) SamplePayload(rng *rand.Rand) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.payloads) == 0 {
		return nil, false
	}
	return r.payloads[rng.Intn(len(r.payloads))], true
}

// Count returns total remembered artifacts (for assertions, not hot paths).
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, list := range r.byTenant {
		n += len(list)
	}
	return n
}
