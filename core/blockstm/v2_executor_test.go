package blockstm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// --- Bug #1: Double FlushToMVStore prevention ---
// When a tx fails validation and is re-executed, FlushToMVStore must only
// be called once (inside execute()). A second call would double balance
// deltas because WriteDelta accumulates.

type mockV2State struct {
	flushCount atomic.Int32
	validated  bool
}

func (s *mockV2State) Validate() bool { return s.validated }
func (s *mockV2State) ValidateCategory() string {
	if s.validated {
		return ""
	}
	return "storage"
}
func (s *mockV2State) IsBaseOnly() bool                        { return false }
func (s *mockV2State) MarkEstimate()                           {}
func (s *mockV2State) CleanupEstimate([]Key, []common.Address) {}
func (s *mockV2State) GetWriteKeys() []Key                     { return nil }
func (s *mockV2State) GetBalAddrs() []common.Address           { return nil }
func (s *mockV2State) FlushToMVStore()                         { s.flushCount.Add(1) }
func (s *mockV2State) SetDeferMVWrites(bool)                   {}

type mockV2Task struct{ idx int }

func (t *mockV2Task) Index() int             { return t.idx }
func (t *mockV2Task) Sender() common.Address { return common.Address{} }
func (t *mockV2Task) To() *common.Address    { return nil }

type mockV2Env struct {
	states []*mockV2State
}

func (e *mockV2Env) BaseNonce(common.Address) uint64 { return 0 }
func (e *mockV2Env) Execute(task V2Task, workerID int, incarnation int,
	senderNonces map[common.Address]uint64, coinbase common.Address,
	waitForTx func(int), waitForFinal func(int), deferWrites bool) V2TxState {
	return e.states[task.Index()]
}
func (e *mockV2Env) Recycle(V2TxState) {}

func TestV2DoubleFlushPrevention(t *testing.T) {
	// Create 2 txs: tx0 passes validation, tx1 fails then passes on re-execution
	states := []*mockV2State{
		{validated: true},  // tx0 always passes
		{validated: false}, // tx1 fails first time
	}
	env := &mockV2Env{states: states}
	tasks := []V2Task{&mockV2Task{0}, &mockV2Task{1}}

	settleCount := 0
	settleFn := func(txIdx int, st V2TxState) {
		settleCount++
		ms := st.(*mockV2State)
		// After first validation failure, make tx1 pass on re-execution
		if txIdx == 1 {
			ms.validated = true
		}
	}

	result := ExecuteV2BlockSTM(context.Background(), tasks, env, common.Address{}, 1, nil, settleFn)

	// tx0: executed once, flushed once
	if states[0].flushCount.Load() != 1 {
		t.Errorf("tx0 FlushToMVStore called %d times, want 1", states[0].flushCount.Load())
	}

	// tx1: executed twice (initial + re-exec), flushed once per execution = 2
	if states[1].flushCount.Load() != 2 {
		t.Errorf("tx1 FlushToMVStore called %d times, want 2 (once per execution)", states[1].flushCount.Load())
	}

	if result.VFailCount != 1 {
		t.Errorf("VFailCount = %d, want 1", result.VFailCount)
	}
	if settleCount != 2 {
		t.Errorf("settleCount = %d, want 2", settleCount)
	}
}

// timedMockV2State extends mockV2State with optional execution delay and
// per-tx timestamp recording, so tests can observe the wall-clock ordering
// of initial executions and re-execution dispatches.
type timedMockV2State struct {
	mockV2State
	failsRemaining int32 // atomic; decrements on each failed Validate()
	mu             struct {
		sync.Mutex
		execStarts    []time.Time
		execEnds      []time.Time
		validateCalls []time.Time
	}
}

func (s *timedMockV2State) Validate() bool {
	s.mu.Lock()
	s.mu.validateCalls = append(s.mu.validateCalls, time.Now())
	s.mu.Unlock()
	if atomic.LoadInt32(&s.failsRemaining) > 0 {
		atomic.AddInt32(&s.failsRemaining, -1)
		return false
	}
	return true
}

func (s *timedMockV2State) ValidateCategory() string {
	if atomic.LoadInt32(&s.failsRemaining) > 0 {
		return "storage"
	}
	return ""
}

// timedMockV2Env supplies fresh timedMockV2States and runs each tx's
// "execution" as a sleep of execDelay. It records start/end timestamps
// keyed by tx index so the test can compare the timeline of tx i's
// initial vs tx j's re-execution dispatch.
type timedMockV2Env struct {
	delays []time.Duration
	states []*timedMockV2State
}

func (e *timedMockV2Env) BaseNonce(common.Address) uint64 { return 0 }
func (e *timedMockV2Env) Execute(task V2Task, workerID int, incarnation int,
	senderNonces map[common.Address]uint64, coinbase common.Address,
	waitForTx func(int), waitForFinal func(int), deferWrites bool) V2TxState {
	idx := task.Index()
	s := e.states[idx]
	s.mu.Lock()
	s.mu.execStarts = append(s.mu.execStarts, time.Now())
	s.mu.Unlock()
	if e.delays[idx] > 0 {
		time.Sleep(e.delays[idx])
	}
	s.mu.Lock()
	s.mu.execEnds = append(s.mu.execEnds, time.Now())
	s.mu.Unlock()
	return s
}
func (e *timedMockV2Env) Recycle(V2TxState) {}

// TestV2ValidationLoopSerializationBlocksReexec verifies that V2's strict
// in-order validation loop delays a failing tx's re-execution until the
// validator has walked past every earlier tx — even when those earlier
// txs are independent and slow. The test demonstrates a real bottleneck
// the design accepts: re-execution of tx N cannot start until
// validateOne(N) has run, and validateOne(N) cannot run until
// validateOne(0..N-1) have all completed.
//
// Setup:
//   - tx0: slow initial execution (200ms), validates successfully
//   - tx1: instant initial execution, fails validation once → needs re-exec
//
// Both run on the worker pool concurrently from the start, so tx1's
// initial execution finishes long before tx0's. In an "ideal" parallel
// design, tx1's re-execution could start right after its initial
// completes (microseconds). In V2, it has to wait for validateOne(0),
// which blocks on tx0's slow initial.
//
// Expected observation: time(tx1 reexec start) - time(tx1 initial end)
// ≈ tx0's execDelay (~200ms), not ≈0.
func TestV2ValidationLoopSerializationBlocksReexec(t *testing.T) {
	const slow = 200 * time.Millisecond

	states := []*timedMockV2State{
		{}, // tx0: passes immediately
		{}, // tx1: fails once
	}
	atomic.StoreInt32(&states[1].failsRemaining, 1)

	env := &timedMockV2Env{
		delays: []time.Duration{slow, 0},
		states: states,
	}
	tasks := []V2Task{&mockV2Task{0}, &mockV2Task{1}}

	// Plenty of workers so initial execs of tx0 and tx1 can run together.
	_ = ExecuteV2BlockSTM(context.Background(), tasks, env, common.Address{}, 4, nil, func(int, V2TxState) {})

	// Pull the timestamps. Tx1 was "executed" twice — initial + re-exec.
	states[1].mu.Lock()
	defer states[1].mu.Unlock()
	if len(states[1].mu.execStarts) != 2 {
		t.Fatalf("tx1 expected 2 execution starts, got %d", len(states[1].mu.execStarts))
	}
	tx1InitialEnd := states[1].mu.execEnds[0]
	tx1ReexecStart := states[1].mu.execStarts[1]
	gap := tx1ReexecStart.Sub(tx1InitialEnd)

	t.Logf("tx0 initial took ~%s (slow)", slow)
	t.Logf("tx1 initial→reexec gap: %s", gap)

	// tx1's re-execution should be delayed by approximately tx0's slow
	// initial execution. Allow a generous tolerance (CI variance), but
	// require the gap to be a meaningful fraction of tx0's delay so we're
	// actually demonstrating the bottleneck.
	if gap < slow/2 {
		t.Errorf("tx1 re-exec started only %s after its initial — expected at least %s "+
			"(should be blocked behind tx0's %s initial via validateOne(0))",
			gap, slow/2, slow)
	}
	// And it shouldn't be *more* than tx0's delay plus a little — that would
	// indicate something else is wrong.
	if gap > slow+200*time.Millisecond {
		t.Errorf("tx1 re-exec gap %s far exceeds tx0's slow initial %s — unexpected delay",
			gap, slow)
	}
}
