package control

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

const deviceLoginRetention = 5 * time.Minute

type DeviceAuth interface {
	Start(context.Context) (codex.DeviceAuthorization, <-chan codex.DeviceAuthorizationResult, error)
	Cancel(string)
}

type deviceLoginAttempt struct {
	status     string
	message    string
	expiresAt  time.Time
	finalizing bool
	context    context.Context
	cancel     context.CancelFunc
	timer      *time.Timer
}

func (s *Server) startDeviceLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DisplayName string `json:"display_name"`
	}
	if !decode(w, r, &request) {
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	if request.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	authorization, result, err := s.deviceAuth.Start(r.Context())
	if err != nil {
		slog.Warn("failed to start Codex device authorization", "error", err)
		writeError(w, http.StatusBadGateway, "failed to start Codex device authorization")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	attempt := &deviceLoginAttempt{status: "pending", expiresAt: authorization.ExpiresAt, context: ctx, cancel: cancel}
	s.loginMu.Lock()
	s.logins[authorization.LoginID] = attempt
	attempt.timer = time.AfterFunc(until(authorization.ExpiresAt), func() {
		s.expireDeviceLogin(authorization.LoginID, attempt)
	})
	s.loginMu.Unlock()
	go s.finishDeviceLogin(authorization.LoginID, request.DisplayName, attempt, result)
	writeJSON(w, http.StatusCreated, authorization)
}

func (s *Server) getDeviceLogin(w http.ResponseWriter, r *http.Request) {
	loginID := r.PathValue("id")
	s.loginMu.Lock()
	attempt, ok := s.logins[loginID]
	expired := ok && attempt.status == "pending" && !attempt.finalizing && time.Now().After(attempt.expiresAt)
	s.loginMu.Unlock()
	if expired {
		s.expireDeviceLogin(loginID, attempt)
	}
	s.loginMu.Lock()
	attempt, ok = s.logins[loginID]
	status, message := "", ""
	if ok {
		status, message = attempt.status, attempt.message
	}
	s.loginMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "device authorization was not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "message": message})
}

func (s *Server) cancelDeviceLogin(w http.ResponseWriter, r *http.Request) {
	loginID := r.PathValue("id")
	s.loginMu.Lock()
	attempt, ok := s.logins[loginID]
	var cancel context.CancelFunc
	if ok {
		delete(s.logins, loginID)
		if attempt.timer != nil {
			attempt.timer.Stop()
		}
		cancel = attempt.cancel
		attempt.cancel = nil
	}
	s.loginMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.deviceAuth.Cancel(loginID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) finishDeviceLogin(loginID, displayName string, attempt *deviceLoginAttempt, results <-chan codex.DeviceAuthorizationResult) {
	result, ok := <-results
	if !ok {
		result.Err = errors.New("Codex device authorization ended without a result")
	}
	s.loginMu.Lock()
	current, exists := s.logins[loginID]
	if !exists || current != attempt || attempt.status != "pending" {
		s.loginMu.Unlock()
		return
	}
	attempt.finalizing = true
	if attempt.timer != nil {
		attempt.timer.Stop()
	}
	s.loginMu.Unlock()

	status := "completed"
	message := "Codex account connected."
	if result.Err != nil {
		slog.Warn("Codex device authorization failed", "error", result.Err)
		status = "failed"
		message = "Codex authorization failed. Start again and confirm the code before it expires."
	} else {
		ctx, cancel := context.WithTimeout(attempt.context, 15*time.Second)
		_, result.Err = s.saveCodexAccount(ctx, result.Credentials, displayName)
		cancel()
		if result.Err != nil {
			if errors.Is(result.Err, context.Canceled) && attempt.context.Err() != nil {
				return
			}
			slog.Error("authorized Codex account could not be saved", "error", result.Err)
			status = "failed"
			switch {
			case errors.Is(result.Err, store.ErrConflict):
				message = "This Codex account is already connected."
			case errors.Is(result.Err, errProviderCredentialsRejected):
				message = "Codex rejected the authorized credentials."
			default:
				message = "The account was authorized but could not be saved. Start again."
			}
		}
	}

	s.loginMu.Lock()
	current, exists = s.logins[loginID]
	if !exists || current != attempt {
		s.loginMu.Unlock()
		return
	}
	attempt.status = status
	attempt.message = message
	attempt.finalizing = false
	attempt.expiresAt = time.Now().Add(deviceLoginRetention)
	cancel := attempt.cancel
	attempt.cancel = nil
	s.scheduleDeviceLoginRemovalLocked(loginID, attempt)
	s.loginMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) expireDeviceLogin(loginID string, attempt *deviceLoginAttempt) {
	s.loginMu.Lock()
	current, exists := s.logins[loginID]
	if !exists || current != attempt || attempt.status != "pending" || attempt.finalizing {
		s.loginMu.Unlock()
		return
	}
	attempt.status = "failed"
	attempt.message = "The authorization code expired. Start again."
	attempt.expiresAt = time.Now().Add(deviceLoginRetention)
	cancel := attempt.cancel
	attempt.cancel = nil
	s.scheduleDeviceLoginRemovalLocked(loginID, attempt)
	s.loginMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.deviceAuth.Cancel(loginID)
}

func (s *Server) scheduleDeviceLoginRemovalLocked(loginID string, attempt *deviceLoginAttempt) {
	if attempt.timer != nil {
		attempt.timer.Stop()
	}
	attempt.timer = time.AfterFunc(until(attempt.expiresAt), func() {
		s.loginMu.Lock()
		current, exists := s.logins[loginID]
		if exists && current == attempt && !time.Now().Before(attempt.expiresAt) {
			delete(s.logins, loginID)
		}
		s.loginMu.Unlock()
	})
}

func until(deadline time.Time) time.Duration {
	delay := time.Until(deadline)
	if delay < 0 {
		return 0
	}
	return delay
}
