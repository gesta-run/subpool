package store

import (
	"context"
	"time"

	"github.com/gesta-run/subpool/internal/store/storedb"
)

func (p *Postgres) PutAntigravitySignature(ctx context.Context, accountID, model, callID string, ciphertext []byte, expires time.Time) error {
	return wrapDB("save Antigravity signature", p.queries.PutAntigravitySignature(ctx, storedb.PutAntigravitySignatureParams{
		ProviderAccountID: accountID, Model: model, CallID: callID, Ciphertext: ciphertext, ExpiresAt: dbTime(expires)}))
}

func (p *Postgres) GetAntigravitySignature(ctx context.Context, accountID, model, callID string) ([]byte, error) {
	raw, err := p.queries.GetAntigravitySignature(ctx, storedb.GetAntigravitySignatureParams{ProviderAccountID: accountID, Model: model, CallID: callID})
	return raw, wrapDB("read Antigravity signature", err)
}
