package sqlite

import (
	"context"
	"errors"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var errNotImplemented = errors.New("not implemented")

func (db *DB) LeaseOutbox(ctx context.Context, botID string, limit int, now time.Time, lease time.Duration) ([]store.OutboxRecord, error) {
	return nil, errNotImplemented
}

func (db *DB) GetOutbox(ctx context.Context, botID, actionID string) (store.OutboxRecord, error) {
	return store.OutboxRecord{}, errNotImplemented
}

func (db *DB) AckOutbox(ctx context.Context, botID, actionID string, ack conloomapi.AckRequest, botMsg *store.MessageRecord, now time.Time) (bool, error) {
	return false, errNotImplemented
}

func (db *DB) ExpireOutbox(ctx context.Context, now time.Time) (int, error) {
	return 0, errNotImplemented
}

func (db *DB) LatestPersona(ctx context.Context, convKey string) (store.PersonaVersion, error) {
	return store.PersonaVersion{}, errNotImplemented
}

func (db *DB) GetPersonaVersion(ctx context.Context, convKey string, version int) (store.PersonaVersion, error) {
	return store.PersonaVersion{}, errNotImplemented
}

func (db *DB) AppendPersonaVersion(ctx context.Context, pv store.PersonaVersion) (store.PersonaVersion, error) {
	return store.PersonaVersion{}, errNotImplemented
}

func (db *DB) LatestPolicy(ctx context.Context, convKey string) (store.PolicyVersion, error) {
	return store.PolicyVersion{}, errNotImplemented
}

func (db *DB) ListPolicyVersions(ctx context.Context, convKey string) ([]store.PolicyVersion, error) {
	return nil, errNotImplemented
}

func (db *DB) AppendPolicyVersion(ctx context.Context, pv store.PolicyVersion) (store.PolicyVersion, error) {
	return store.PolicyVersion{}, errNotImplemented
}

func (db *DB) InsertAudit(ctx context.Context, a store.AuditEvent) error {
	return errNotImplemented
}

func (db *DB) Backup(ctx context.Context, path string) error {
	return errNotImplemented
}
