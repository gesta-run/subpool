package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/gateway/responsesws"
	"github.com/gesta-run/subpool/internal/provider/codex"
)

type responsesWSBackend struct{ server *Server }

func (b responsesWSBackend) Authenticate(ctx context.Context, authorization, requiredScope string) (domain.KeyRoute, []byte, *responsesws.RequestError) {
	route, requestErr := b.server.authenticate(ctx, authorization, requiredScope)
	if requestErr != nil {
		return domain.KeyRoute{}, nil, &responsesws.RequestError{Status: requestErr.status, Message: requestErr.message, Code: requestErr.code}
	}
	plain, err := auth.Bearer(authorization)
	if err != nil {
		return domain.KeyRoute{}, nil, &responsesws.RequestError{Status: http.StatusUnauthorized, Message: "invalid API key", Code: "invalid_api_key"}
	}
	return route, b.server.keys.Digest(plain), nil
}

func (b responsesWSBackend) AllowAPIKeyRequest(ctx context.Context, route domain.KeyRoute) *responsesws.RequestError {
	requestErr := b.server.allowAPIKeyRequest(ctx, route)
	if requestErr == nil {
		return nil
	}
	return &responsesws.RequestError{Status: requestErr.status, Message: requestErr.message, Code: requestErr.code}
}

func (b responsesWSBackend) RoutingStore() responsesws.RoutingStore { return b.server.store }

func (b responsesWSBackend) Now() time.Time { return b.server.now() }

func (b responsesWSBackend) AccountHealthy(account domain.ProviderAccount) bool {
	return accountHealthy(account, b.server.now())
}

func (b responsesWSBackend) CodexCredentials(account domain.ProviderAccount) (codex.Credentials, error) {
	credentials, err := b.server.credentials(account)
	return credentials.codex, err
}

func (b responsesWSBackend) RefreshAccount(ctx context.Context, account domain.ProviderAccount) (domain.ProviderAccount, error) {
	return b.server.refresh(ctx, account)
}

func (b responsesWSBackend) RecordRefreshFailure(ctx context.Context, accountID string, err error) bool {
	return b.server.recordRefreshFailure(ctx, accountID, err)
}

func (b responsesWSBackend) RecordHealthFailure(ctx context.Context, accountID, code string) {
	b.server.recordHealthFailure(ctx, accountID, code)
}

func (b responsesWSBackend) RetryAfter(header http.Header) time.Time {
	return retryAfter(header, b.server.now())
}

func (b responsesWSBackend) AttemptBridge(ctx context.Context, headers http.Header, route domain.KeyRoute, account domain.ProviderAccount, model string, payload []byte) (*http.Response, responsesws.RetryReason, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://subpool.local/v1/responses", nil)
	if err != nil {
		return nil, responsesws.RetryUnavailable, false
	}
	request.Header = headers
	response, retry, complete := b.server.attemptAccount(request, route, upstreamRequest{
		kind: "responses", model: model, body: payload, codexBody: payload,
	}, account)
	return response, responsesws.RetryReason(retry), complete
}

func (b responsesWSBackend) CompleteTurn(keyID, poolID, accountID, responseID, model string, input, output int64) {
	if input > 0 || output > 0 {
		b.server.addUsage(keyID, b.server.usageEventHash(responseID, b.server.randomUsageEventHash()), model, input, output)
	}
	if responseID != "" {
		b.server.saveSession(keyID, poolID, responseID, accountID)
	}
	if b.server.activity.ShouldRecord(accountID, keyID, b.server.now()) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = b.server.store.RecordRequestSuccess(ctx, accountID, keyID, b.server.now())
		cancel()
	}
}

func (b responsesWSBackend) NormalizeCodexRequest(body []byte, account domain.ProviderAccount) ([]byte, *responsesws.RequestError) {
	installationID, err := codex.InstallationID(account.ID)
	if err != nil {
		return nil, &responsesws.RequestError{
			Status: http.StatusInternalServerError, Message: "device identity is unavailable", Code: "server_error",
		}
	}
	payload, err := normalizeCodexWebSocketRequest(body, installationID, account.FastModeEnabled)
	if err != nil {
		return nil, &responsesws.RequestError{
			Status: http.StatusBadRequest, Message: err.Error(), Code: "invalid_request_error",
		}
	}
	return payload, nil
}

func (b responsesWSBackend) ReadRequestBody(reader io.Reader) ([]byte, func(), error) {
	body, reservation, err := b.server.readBufferedBody(reader, -1, responsesWSRequestBodyCopies)
	if errors.Is(err, errRequestBodyTooLarge) {
		return nil, nil, responsesws.ErrRequestBodyTooLarge
	}
	if errors.Is(err, errRequestBodyCapacity) {
		return nil, nil, responsesws.ErrRequestBodyCapacity
	}
	if err != nil {
		return nil, nil, err
	}
	return body, reservation.release, nil
}

func (b responsesWSBackend) RequestBodyState() responsesws.RequestBodyState {
	return responsesws.RequestBodyState{
		MaxBytes:         b.server.maxRequestBodyBytes,
		ReadTimeout:      b.server.requestBodyTimeout,
		InflightBytes:    b.server.requestBodyBudget.used.Load(),
		MaxInflightBytes: b.server.requestBodyBudget.limit,
	}
}

func (s *Server) CloseResponsesWebSockets() { s.responsesWS.CloseAll() }

func (s *Server) CloseResponsesWebSocketsForAccount(accountID string) {
	s.responsesWS.CloseAccount(accountID)
}

func (s *Server) ResponsesWebSocketMetrics() string { return s.responsesWS.Metrics() }
