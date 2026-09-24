//go:build race

package markdown

// The race detector slows rendering about tenfold, so timing checks allow
// for it. Ordinary test runs keep the real budget.
const budgetScale = 10

// The race detector keeps shadow memory beside the heap, several times its
// size, which the child's heap limit does not see.
const memoryScale = 4
