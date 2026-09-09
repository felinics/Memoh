package sessionruntime

import "time"

// Scan sizing shapes one statement rather than describing a deployment, so it
// lives here as a constant instead of in config.toml. Tests lower it through
// Options to observe that recovery really is batched.
const (
	defaultScanBatchSize            = 200
	defaultMaxScanBatchesPerTick    = 5
	defaultDurableFinishRetryBudget = time.Minute
)

// tuning holds every session runtime timing that is derived rather than
// configured. Only two values reach TOML — the topology flag and
// backend_loss_grace — because only those depend on something the process
// cannot observe. Everything below is a function of owner_lease_ttl, which is
// the one number describing how tolerant the environment is of pauses.
//
// Coupling these to a single dial is deliberate: a deployment cannot ask for
// slower expiry detection with a faster reaper tick, but neither can it set the
// leader lease shorter than the tick that renews it.
type tuning struct {
	// ownerLeaseTTL is the lease an owner must renew to keep its run.
	ownerLeaseTTL time.Duration
	// backendLossGrace is the delay between observing a new liveness
	// generation and starting the fail-closed sweep.
	backendLossGrace time.Duration
	// reaperTick is the leader's cadence, matching lease renewal.
	reaperTick time.Duration
	// reaperLeaderLeaseTTL has the same semantics as an owner lease and is
	// renewed the same way, so it is the same duration.
	reaperLeaderLeaseTTL time.Duration
	// orphanGrace is the minimum age before an unowned accepted row is treated
	// as abandoned. The window between the durable insert and the live
	// reservation is milliseconds, so this only needs to clear normal latency.
	orphanGrace time.Duration
	// durableFinishRetryBudget bounds how long an owner keeps renewing a run
	// while PostgreSQL terminal writes fail. After it expires the owner drops
	// local control so lease expiry hands convergence to the reaper.
	durableFinishRetryBudget time.Duration

	scanBatchSize         int32
	maxScanBatchesPerTick int
}

func newTuning(ownerLeaseTTL, backendLossGrace time.Duration, scanBatchSize int32, maxScanBatchesPerTick int) tuning {
	if ownerLeaseTTL <= 0 {
		ownerLeaseTTL = 30 * time.Second
	}
	if backendLossGrace < ownerLeaseTTL {
		backendLossGrace = backendLossGraceFactor * ownerLeaseTTL
	}
	if scanBatchSize <= 0 {
		scanBatchSize = defaultScanBatchSize
	}
	if maxScanBatchesPerTick <= 0 {
		maxScanBatchesPerTick = defaultMaxScanBatchesPerTick
	}
	return tuning{
		ownerLeaseTTL:            ownerLeaseTTL,
		backendLossGrace:         backendLossGrace,
		reaperTick:               leaseRenewInterval(ownerLeaseTTL),
		reaperLeaderLeaseTTL:     ownerLeaseTTL,
		orphanGrace:              orphanGraceFactor * ownerLeaseTTL,
		durableFinishRetryBudget: defaultDurableFinishRetryBudget,
		scanBatchSize:            scanBatchSize,
		maxScanBatchesPerTick:    maxScanBatchesPerTick,
	}
}

const (
	backendLossGraceFactor = 3
	orphanGraceFactor      = 3
)
