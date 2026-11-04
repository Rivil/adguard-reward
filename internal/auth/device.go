package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

var (
	// ErrDeviceNotFound is returned by a DeviceStore when no device row
	// matches.
	ErrDeviceNotFound = errors.New("device not found")
	// ErrNoAttempts is returned by ReserveAttempt when the device has
	// already used all MaxPINAttempts: the caller treats it as revoked.
	ErrNoAttempts = errors.New("no pin attempts left")
)

// MaxPINAttempts is how many consecutive wrong PINs a device survives; the
// last of them revokes it. The devices table CHECK caps the counter here.
const MaxPINAttempts = 5

// Device is one browser with a PIN. The raw device token lives only in the
// cookie; TokenHash is its SHA-256 and PINHash an Argon2id PHC string.
type Device struct {
	ID             int64
	Username       string
	TokenHash      [32]byte
	PINHash        string
	FailedAttempts int
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// LogValue implements slog.LogValuer: only the id and username are logged,
// never the token or PIN hash.
func (d Device) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int64("id", d.ID),
		slog.String("username", d.Username),
	)
}

// DeviceStore persists devices. internal/store implements it over SQLite.
type DeviceStore interface {
	// InsertDevice stores d (ID and FailedAttempts ignored; the counter
	// starts at 0) and returns the assigned id.
	InsertDevice(ctx context.Context, d Device) (id int64, err error)
	// DeviceByTokenHash returns the device with this hash or
	// ErrDeviceNotFound.
	DeviceByTokenHash(ctx context.Context, hash [32]byte) (Device, error)
	// DeviceByID returns the device or ErrDeviceNotFound.
	DeviceByID(ctx context.Context, id int64) (Device, error)
	// ReserveAttempt atomically increments the device's failed-attempt
	// counter before the PIN is checked and returns the new count
	// (1..MaxPINAttempts). A device already at the cap returns
	// ErrNoAttempts; a missing one ErrDeviceNotFound.
	ReserveAttempt(ctx context.Context, id int64) (attempt int, err error)
	// ResetAttempts sets the counter back to 0 after a correct PIN.
	ResetAttempts(ctx context.Context, id int64) error
	// ReplacePIN swaps the PIN hash and resets the counter; the token is
	// kept. A missing device returns ErrDeviceNotFound.
	ReplacePIN(ctx context.Context, id int64, pinHash string) error
	// TouchDevice sets expires_at (the sliding 90-day lifetime).
	TouchDevice(ctx context.Context, id int64, expires time.Time) error
	// RevokeDevice deletes the device and every session tagged with it,
	// in one transaction.
	RevokeDevice(ctx context.Context, id int64) error
	// DeleteDevice deletes only the device; its sessions survive with
	// device_id cleared.
	DeleteDevice(ctx context.Context, id int64) error
	// DeleteOtherDevices deletes every device of username except keepID
	// (0 keeps none) and returns how many went.
	DeleteOtherDevices(ctx context.Context, username string, keepID int64) (int, error)
	// DeleteExpiredDevices deletes devices whose expires_at <= now, and
	// the sessions tagged with them, and returns how many devices went.
	DeleteExpiredDevices(ctx context.Context, now time.Time) (int, error)
}
