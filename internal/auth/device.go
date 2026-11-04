package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
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

// DeviceCookieName is the device cookie, separate from the session cookie
// so a lapsed session can still name the device that may unlock.
const DeviceCookieName = "adguard_reward_device"

// DefaultDeviceLifetime is the locked 90-day device token lifetime.
const DefaultDeviceLifetime = 90 * 24 * time.Hour

// CodePINRequired is RequireSession's 401 code when the session has lapsed
// but the request carries a live device cookie.
const CodePINRequired = "pin_required"

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

// IssueDevice creates a device for username with the given PIN hash and
// returns the raw token for the device cookie: 256 random bits, of which
// only the SHA-256 is stored. The token exists only in the return value.
func (m *Manager) IssueDevice(ctx context.Context, username, pinHash string) (string, Device, error) {
	if m.devices == nil {
		return "", Device{}, errors.New("no device store")
	}
	raw, hash, err := newToken()
	if err != nil {
		return "", Device{}, err
	}
	now := m.now()
	d := Device{
		Username:  username,
		TokenHash: hash,
		PINHash:   pinHash,
		CreatedAt: now,
		ExpiresAt: now.Add(m.devLife),
	}
	id, err := m.devices.InsertDevice(ctx, d)
	if err != nil {
		return "", Device{}, err
	}
	d.ID = id
	m.log.Debug("device issued", "device", d)
	return raw, d, nil
}

// LookupDevice resolves a presented device token. Malformed, unknown and
// expired tokens all return ErrDeviceNotFound; an expired device is revoked
// on sight, its tagged sessions with it. Lookup never slides the expiry —
// only a successful unlock does. Without a device store every token is
// ErrDeviceNotFound.
func (m *Manager) LookupDevice(ctx context.Context, raw string) (Device, error) {
	if m.devices == nil {
		return Device{}, ErrDeviceNotFound
	}
	hash, ok := hashToken(raw)
	if !ok {
		m.log.Debug("device rejected", "reason", "malformed token")
		return Device{}, ErrDeviceNotFound
	}
	d, err := m.devices.DeviceByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrDeviceNotFound) {
			m.log.Debug("device rejected", "reason", "unknown token")
		}
		return Device{}, err
	}
	if !d.ExpiresAt.After(m.now()) {
		if err := m.devices.RevokeDevice(ctx, d.ID); err != nil {
			return Device{}, err
		}
		m.log.Debug("device rejected", "reason", "expired", "device", d)
		return Device{}, ErrDeviceNotFound
	}
	return d, nil
}

// DeviceLifetime is how long a device lives without an unlock.
func (m *Manager) DeviceLifetime() time.Duration { return m.devLife }

// DeviceCookie is the device cookie carrying raw.
func (m *Manager) DeviceCookie(raw string) *http.Cookie {
	return &http.Cookie{
		Name:     DeviceCookieName,
		Value:    raw,
		Path:     "/",
		MaxAge:   int(m.devLife / time.Second),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteStrictMode,
	}
}

// ClearDeviceCookie is the cookie that deletes the device cookie.
func (m *Manager) ClearDeviceCookie() *http.Cookie {
	c := m.DeviceCookie("")
	c.MaxAge = -1
	return c
}

// SetDeviceCookie writes c, replacing any Set-Cookie for the device cookie
// already on w so a response never carries two.
func SetDeviceCookie(w http.ResponseWriter, c *http.Cookie) {
	dropCookie(w.Header(), DeviceCookieName)
	http.SetCookie(w, c)
}

// ClearDevice writes the clearing device cookie, replacing any already on w.
func (m *Manager) ClearDevice(w http.ResponseWriter) {
	SetDeviceCookie(w, m.ClearDeviceCookie())
}
