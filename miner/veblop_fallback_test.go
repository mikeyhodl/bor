package miner

import "testing"

// TestDecideVeblopFallback covers the producer stall-fallback decision logic in
// newWorkLoop's veblop-timer branch.
//
// Regression context (mainnet incident): a block producer froze because a build
// wedged inside commitWork left pendingWorkBlock pinned at currentBlock+1 with no
// sealing task (pendingTasks empty). The old guard skipped recovery on
// `pendingWorkBlock == nextBlock` alone, so the producer never resubmitted work
// and only a restart recovered.
//
// The fix must ALSO not interrupt a build that is merely slow-but-live (same
// pendingWorkBlock==next, no-task state, but progressing). The two are told apart
// by how long the build has been outstanding (pendingWorkAgeSec) — never by
// block-timestamp age (chainAgeSec), which is meaningless when the head carries
// an old timestamp (e.g. genesis).
func TestDecideVeblopFallback(t *testing.T) {
	const (
		current    = uint64(100)
		next       = current + 1
		timeout    = int64(1) // veblop timeout (block period) in seconds
		stallAfter = 3 * timeout
		bigAge     = int64(1_000_000) // genesis-like huge chain age
	)

	tests := []struct {
		name              string
		pendingWorkBlock  uint64
		hasPendingTasks   bool
		chainAgeSec       int64
		pendingWorkAgeSec int64
		want              veblopFallbackDecision
	}{
		{
			// THE BUG: build wedged in commitWork pinned pendingWorkBlock=next,
			// produced no task, and has been outstanding well past a normal build.
			name:              "wedged build, outstanding past threshold -> recommit",
			pendingWorkBlock:  next,
			hasPendingTasks:   false,
			chainAgeSec:       bigAge,
			pendingWorkAgeSec: 5 * timeout,
			want:              veblopRecommit,
		},
		{
			// THE REGRESSION GUARD: at genesis chainAge is huge, but the build was
			// just submitted and is still in progress — must NOT be interrupted.
			name:              "in-progress build at genesis (huge chainAge, fresh build) -> wait",
			pendingWorkBlock:  next,
			hasPendingTasks:   false,
			chainAgeSec:       bigAge,
			pendingWorkAgeSec: 0,
			want:              veblopWait,
		},
		{
			name:              "task in flight for next block -> skip",
			pendingWorkBlock:  next,
			hasPendingTasks:   true,
			chainAgeSec:       bigAge,
			pendingWorkAgeSec: 10 * timeout,
			want:              veblopSkip,
		},
		{
			// Nothing claimed for next block and chain stale -> resubmit.
			name:              "no work claimed, stale -> recommit",
			pendingWorkBlock:  0,
			hasPendingTasks:   false,
			chainAgeSec:       timeout,
			pendingWorkAgeSec: 0,
			want:              veblopRecommit,
		},
		{
			// Nothing claimed but chain still fresh -> wait.
			name:              "no work claimed, fresh -> wait",
			pendingWorkBlock:  0,
			hasPendingTasks:   false,
			chainAgeSec:       timeout - 1,
			pendingWorkAgeSec: 0,
			want:              veblopWait,
		},
		{
			// A task is sealing and pendingWorkBlock was already cleared by
			// commitWork's deferred reset -> never resubmit on top of it.
			name:              "task sealing, pendingWorkBlock cleared -> skip",
			pendingWorkBlock:  0,
			hasPendingTasks:   true,
			chainAgeSec:       bigAge,
			pendingWorkAgeSec: 0,
			want:              veblopSkip,
		},
		{
			name:              "wedge boundary: outstanding exactly at threshold -> recommit",
			pendingWorkBlock:  next,
			hasPendingTasks:   false,
			chainAgeSec:       bigAge,
			pendingWorkAgeSec: stallAfter,
			want:              veblopRecommit,
		},
		{
			name:              "wedge boundary: just below threshold -> wait",
			pendingWorkBlock:  next,
			hasPendingTasks:   false,
			chainAgeSec:       bigAge,
			pendingWorkAgeSec: stallAfter - 1,
			want:              veblopWait,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideVeblopFallback(tt.pendingWorkBlock, next, tt.hasPendingTasks, tt.chainAgeSec, timeout, tt.pendingWorkAgeSec, stallAfter)
			if got != tt.want {
				t.Fatalf("decideVeblopFallback(pwb=%d, next=%d, hasTasks=%v, chainAge=%d, timeout=%d, workAge=%d, stallAfter=%d) = %d, want %d",
					tt.pendingWorkBlock, next, tt.hasPendingTasks, tt.chainAgeSec, timeout, tt.pendingWorkAgeSec, stallAfter, got, tt.want)
			}
		})
	}
}
