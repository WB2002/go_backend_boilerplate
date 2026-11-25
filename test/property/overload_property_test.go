package property_test

import (
	"testing"
	"testing/quick"

	"go_backend_boilerplate/internal/core/overload"
)

func TestOverloadPriorityMonotonicityProperty(t *testing.T) {
	checkMonotonic := func(rawMax uint16, rawLoad uint16) bool {
		maxInFlight := int(rawMax%200) + 4
		currentLoad := int(rawLoad % uint16(maxInFlight+20))

		manager := overload.NewManager(overload.Config{
			MaxInFlight:             maxInFlight,
			PrioritySheddingEnabled: true,
			RetryAfterSec:           5,
		})
		defer manager.Stop()

		for i := 0; i < currentLoad; i++ {
			manager.Acquire()
		}

		shedLow := manager.ShouldShed(overload.PriorityLow)
		shedNormal := manager.ShouldShed(overload.PriorityNormal)
		shedHigh := manager.ShouldShed(overload.PriorityHigh)
		shedCritical := manager.ShouldShed(overload.PriorityCritical)

		// Priority shedding must be monotonic: shedding a higher priority
		// must imply all lower priorities are also shed.
		if shedCritical && !shedHigh {
			return false
		}
		if shedHigh && !shedNormal {
			return false
		}
		if shedNormal && !shedLow {
			return false
		}
		return true
	}

	if err := quick.Check(checkMonotonic, &quick.Config{MaxCount: 250}); err != nil {
		t.Fatalf("overload priority monotonicity violated: %v", err)
	}
}

func TestOverloadAcquireReleaseBalanceProperty(t *testing.T) {
	checkBalance := func(rawMax uint8, rawAcquires uint8) bool {
		maxInFlight := int(rawMax%50) + 1
		attempts := int(rawAcquires % 80)

		manager := overload.NewManager(overload.Config{
			MaxInFlight:             maxInFlight,
			PrioritySheddingEnabled: true,
			RetryAfterSec:           5,
		})
		defer manager.Stop()

		acquired := 0
		for i := 0; i < attempts; i++ {
			if manager.TryAcquire(overload.PriorityNormal) {
				acquired++
			}
		}

		if int(manager.InFlight()) != acquired {
			return false
		}

		for i := 0; i < acquired; i++ {
			manager.Release()
		}

		return manager.InFlight() == 0
	}

	if err := quick.Check(checkBalance, &quick.Config{MaxCount: 250}); err != nil {
		t.Fatalf("overload acquire/release balance violated: %v", err)
	}
}
