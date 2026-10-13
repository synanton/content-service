package storage

import (
	"sync"
	"time"
)

type Metrics struct {
	mu            sync.RWMutex
	putCount      uint64
	getCount      uint64
	putErrors     uint64
	getErrors     uint64
	putLatency    time.Duration
	getLatency    time.Duration
	bytesStored   uint64
}

func NewMetrics() *Metrics {
	return &Metrics{}
}

func (m *Metrics) RecordPut(tier string, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putCount++
	m.bytesStored += uint64(size)
}

func (m *Metrics) RecordGet(tier string, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCount++
}

func (m *Metrics) RecordPutError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putErrors++
}

func (m *Metrics) RecordGetError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getErrors++
}

func (m *Metrics) RecordPutLatency(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putLatency += d
}

func (m *Metrics) RecordGetLatency(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getLatency += d
}

func (m *Metrics) Snapshot() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]interface{}{
		"put_count":    m.putCount,
		"get_count":    m.getCount,
		"put_errors":   m.putErrors,
		"get_errors":   m.getErrors,
		"bytes_stored": m.bytesStored,
	}
}
