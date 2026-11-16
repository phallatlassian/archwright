package store

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrApprovalRefused means the developer's approval credential is unset or
// was not given correctly, so the approval did not happen (ADR 0010).
var ErrApprovalRefused = errors.New("approval refused")

// approvalIterations makes each guess at the credential cost a noticeable
// fraction of a second, which is all the rate limit a loopback API gets.
const approvalIterations = 600_000

// Five wrong guesses in a row lock every check for a minute, so a token holder
// cannot guess the credential online. The count lives in the Works process.
const (
	approvalMaxFailures = 5
	approvalLockout     = time.Minute
)

func hashApproval(secret string, salt []byte, iterations int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, secret, salt, iterations, sha256.Size)
}

// HasApprovalCredential reports whether the developer has set one.
func (s *Store) HasApprovalCredential() (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM approval_credential").Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CheckApprovalCredential returns ErrApprovalRefused unless secret matches
// the credential the developer set.
func (s *Store) CheckApprovalCredential(secret string) error {
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	if now := s.now(); now.Before(s.approvalLockedUntil) {
		return fmt.Errorf("too many wrong approval credentials; try again in %s: %w", s.approvalLockedUntil.Sub(now).Round(time.Second), ErrApprovalRefused)
	}
	var salt, want []byte
	var iterations int
	err := s.db.QueryRow("SELECT salt, hash, iterations FROM approval_credential").Scan(&salt, &want, &iterations)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no approval credential is set; set one at a terminal with `archwright approval set`: %w", ErrApprovalRefused)
	}
	if err != nil {
		return err
	}
	got, err := hashApproval(secret, salt, iterations)
	if err != nil {
		return err
	}
	if secret == "" || subtle.ConstantTimeCompare(got, want) != 1 {
		s.approvalFailures++
		if s.approvalFailures >= approvalMaxFailures {
			s.approvalFailures = 0
			s.approvalLockedUntil = s.now().Add(approvalLockout)
		}
		if err := appendEvent(s.db, s.stamp(), "", "approval.attempt_failed", map[string]bool{"locked": !s.approvalLockedUntil.IsZero() && s.now().Before(s.approvalLockedUntil)}); err != nil {
			return err
		}
		return fmt.Errorf("the approval credential is wrong: %w", ErrApprovalRefused)
	}
	s.approvalFailures = 0
	return nil
}

// SetApprovalCredential sets the credential, or replaces it when current
// matches the one already set. Only the terminal CLI calls it, straight on the
// database; the Works API never sets or replaces the credential (ADR 0010).
func (s *Store) SetApprovalCredential(secret, current string) error {
	if secret == "" {
		return errors.New("the approval credential cannot be empty")
	}
	set, err := s.HasApprovalCredential()
	if err != nil {
		return err
	}
	if set {
		if err := s.CheckApprovalCredential(current); err != nil {
			return err
		}
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := hashApproval(secret, salt, approvalIterations)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := s.stamp()
	if _, err := tx.Exec("INSERT OR REPLACE INTO approval_credential (id, salt, hash, iterations, set_at) VALUES (1,?,?,?,?)", salt, hash, approvalIterations, at); err != nil {
		return err
	}
	if err := appendEvent(tx, at, "", "approval.credential_set", map[string]bool{"replaced": set}); err != nil {
		return err
	}
	return tx.Commit()
}
