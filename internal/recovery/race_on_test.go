//go:build race

package recovery

// The race detector changes how much the runtime allocates (it drops pooled
// buffers at random, among other things), so allocation bounds are checked
// only in ordinary test runs.
const checkAllocations = false
