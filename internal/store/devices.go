package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

// Store is the auth.DeviceStore; a signature drift fails to compile.
var _ auth.DeviceStore = (*Store)(nil)

const deviceCols = `id, username, token_hash, pin_hash, failed_attempts, created_at, expires_at`

func scanDevice(row interface{ Scan(...any) error }) (auth.Device, error) {
	var (
		d                auth.Device
		hash             []byte
		created, expires int64
	)
	if err := row.Scan(&d.ID, &d.Username, &hash, &d.PINHash, &d.FailedAttempts, &created, &expires); err != nil {
		return auth.Device{}, err
	}
	copy(d.TokenHash[:], hash)
	d.CreatedAt = time.Unix(created, 0).UTC()
	d.ExpiresAt = time.Unix(expires, 0).UTC()
	return d, nil
}

// InsertDevice stores d with a zero attempt counter and returns its rowid.
func (st *Store) InsertDevice(ctx context.Context, d auth.Device) (int64, error) {
	res, err := st.db.ExecContext(ctx,
		`INSERT INTO devices (username, token_hash, pin_hash, failed_attempts, created_at, expires_at)
		 VALUES (?, ?, ?, 0, ?, ?)`,
		d.Username, d.TokenHash[:], d.PINHash, d.CreatedAt.Unix(), d.ExpiresAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("insert device: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert device: %w", err)
	}
	return id, nil
}

// DeviceByTokenHash returns the device with this hash or
// auth.ErrDeviceNotFound.
func (st *Store) DeviceByTokenHash(ctx context.Context, hash [32]byte) (auth.Device, error) {
	d, err := scanDevice(st.db.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE token_hash = ?`, hash[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Device{}, auth.ErrDeviceNotFound
	}
	if err != nil {
		return auth.Device{}, fmt.Errorf("device by hash: %w", err)
	}
	return d, nil
}

// DeviceByID returns the device or auth.ErrDeviceNotFound.
func (st *Store) DeviceByID(ctx context.Context, id int64) (auth.Device, error) {
	d, err := scanDevice(st.db.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Device{}, auth.ErrDeviceNotFound
	}
	if err != nil {
		return auth.Device{}, fmt.Errorf("device by id: %w", err)
	}
	return d, nil
}

// ReserveAttempt increments the counter in one statement, guarded by the
// cap, before any PIN is hashed: a burst of parallel guesses can never get
// more than auth.MaxPINAttempts checks, because each one must win its own
// increment first.
func (st *Store) ReserveAttempt(ctx context.Context, id int64) (int, error) {
	var n int
	err := st.db.QueryRowContext(ctx,
		`UPDATE devices SET failed_attempts = failed_attempts + 1
		  WHERE id = ? AND failed_attempts < ?
		  RETURNING failed_attempts`, id, auth.MaxPINAttempts).Scan(&n)
	if err == nil {
		return n, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("reserve attempt: %w", err)
	}
	// Refused: tell an exhausted device from a missing one.
	if _, err := st.DeviceByID(ctx, id); err != nil {
		return 0, err
	}
	return 0, auth.ErrNoAttempts
}

// ResetAttempts zeroes the counter.
func (st *Store) ResetAttempts(ctx context.Context, id int64) error {
	if _, err := st.db.ExecContext(ctx,
		`UPDATE devices SET failed_attempts = 0 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("reset attempts: %w", err)
	}
	return nil
}

// ReplacePIN swaps the PIN hash and zeroes the counter; token_hash stays.
func (st *Store) ReplacePIN(ctx context.Context, id int64, pinHash string) error {
	res, err := st.db.ExecContext(ctx,
		`UPDATE devices SET pin_hash = ?, failed_attempts = 0 WHERE id = ?`, pinHash, id)
	if err != nil {
		return fmt.Errorf("replace pin: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("replace pin: %w", err)
	}
	if n == 0 {
		return auth.ErrDeviceNotFound
	}
	return nil
}

// TouchDevice writes expires_at.
func (st *Store) TouchDevice(ctx context.Context, id int64, expires time.Time) error {
	if _, err := st.db.ExecContext(ctx,
		`UPDATE devices SET expires_at = ? WHERE id = ?`, expires.Unix(), id); err != nil {
		return fmt.Errorf("touch device: %w", err)
	}
	return nil
}

// RevokeDevice deletes the device's tagged sessions and then the device, in
// one transaction. Without the explicit session delete, ON DELETE SET NULL
// would turn a revoked device's live session into an untagged 30-day one.
func (st *Store) RevokeDevice(ctx context.Context, id int64) error {
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE device_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	return nil
}

// DeleteDevice deletes only the device; ON DELETE SET NULL detaches its
// sessions, which carry on untagged.
func (st *Store) DeleteDevice(ctx context.Context, id int64) error {
	if _, err := st.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete device: %w", err)
	}
	return nil
}

// DeleteOtherDevices revokes every device of username except keepID: their
// tagged sessions go with them, as in RevokeDevice, so a revoked phone's
// open session cannot outlive its device as an untagged one.
func (st *Store) DeleteOtherDevices(ctx context.Context, username string, keepID int64) (int, error) {
	var n int64
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE device_id IN
			   (SELECT id FROM devices WHERE username = ? AND id <> ?)`, username, keepID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM devices WHERE username = ? AND id <> ?`, username, keepID)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("delete other devices: %w", err)
	}
	return int(n), nil
}

// DeleteExpiredDevices removes devices whose expires_at <= now together
// with their tagged sessions: an expired device means the next visit is a
// full login, so its sessions must not carry on as untagged 30-day ones.
func (st *Store) DeleteExpiredDevices(ctx context.Context, now time.Time) (int, error) {
	var n int64
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE device_id IN
			   (SELECT id FROM devices WHERE expires_at <= ?)`, now.Unix()); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE expires_at <= ?`, now.Unix())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired devices: %w", err)
	}
	return int(n), nil
}
