package auth

import (
	"fmt"
	"time"
)

const (
	DefaultSessionAbsoluteLifetime = 7 * 24 * time.Hour
	DefaultSessionIdleTimeout      = 24 * time.Hour
	DefaultMaxActiveDevices        = 10
)

// SessionPolicy is the product-level policy for refreshable device sessions.
// AbsoluteLifetime is fixed when a family is created and never slides during
// refresh. IdleTimeout is measured from the last successful refresh. The
// device cap applies to active refresh families, not individual rotated rows.
type SessionPolicy struct {
	AbsoluteLifetime time.Duration
	IdleTimeout      time.Duration
	MaxActiveDevices int
}

func DefaultSessionPolicy() SessionPolicy {
	return SessionPolicy{
		AbsoluteLifetime: DefaultSessionAbsoluteLifetime,
		IdleTimeout:      DefaultSessionIdleTimeout,
		MaxActiveDevices: DefaultMaxActiveDevices,
	}
}

// WithDefaults fills only zero values. Explicit invalid values remain invalid
// so Validate can fail closed instead of silently weakening policy.
func (p SessionPolicy) WithDefaults() SessionPolicy {
	defaults := DefaultSessionPolicy()
	if p.AbsoluteLifetime == 0 {
		p.AbsoluteLifetime = defaults.AbsoluteLifetime
	}
	if p.IdleTimeout == 0 {
		p.IdleTimeout = defaults.IdleTimeout
	}
	if p.MaxActiveDevices == 0 {
		p.MaxActiveDevices = defaults.MaxActiveDevices
	}
	return p
}

func (p SessionPolicy) Validate() error {
	if p.AbsoluteLifetime <= 0 {
		return fmt.Errorf("session absolute lifetime must be positive")
	}
	if p.IdleTimeout <= 0 {
		return fmt.Errorf("session idle timeout must be positive")
	}
	if p.IdleTimeout > p.AbsoluteLifetime {
		return fmt.Errorf("session idle timeout must not exceed absolute lifetime")
	}
	if p.MaxActiveDevices < 1 {
		return fmt.Errorf("maximum active devices must be at least one")
	}
	return nil
}
