package ui

import "sync/atomic"

// Counters for performance diagnostics (BERTH_STATS).
var (
	StatFrames  atomic.Int64
	StatViews   atomic.Int64
	StatUpdates atomic.Int64
)
