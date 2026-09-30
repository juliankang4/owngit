package state

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PushEvent is one successful push: a receive-pack that updated at least one
// ref. Ref and its object IDs describe one of the updated refs, and
// RefsUpdated counts all of them. OldOID is "" when the push created Ref and
// NewOID is "" when it deleted Ref. PushedAt is when OwnGit recorded the
// push, never a commit's author or committer date.
type PushEvent struct {
	// Sequence orders push events: a later push has a larger one, and a
	// number is never used again for this database. RecentPushes and
	// PushesAfter fill it; RecordPush ignores it.
	Sequence     int64
	RepositoryID string
	// RepositoryName is the repository's display name. RecentPushes fills
	// it; RecordPush ignores it.
	RepositoryName string
	Ref            string
	OldOID, NewOID string
	RefsUpdated    int
	Actor          Actor
	PushedAt       time.Time
}

// PushEventsKept is how many push events OwnGit keeps: RecordPush removes
// the older ones in the transaction that adds a new one.
const PushEventsKept = 100

// RecordPush adds a push event and removes all but the newest PushEventsKept
// events, in one transaction, and returns the new event's sequence. Push
// events are machine-local: backups do not carry them.
func (s *Store) RecordPush(ctx context.Context, event PushEvent) (int64, error) {
	if event.RefsUpdated < 1 || event.Ref == "" || event.PushedAt.IsZero() {
		return 0, errors.New("a push event needs a ref, at least one updated ref and a time")
	}
	for _, oid := range []string{event.OldOID, event.NewOID} {
		if oid != "" && !validObjectID(oid) {
			return 0, fmt.Errorf("push event object ID %q is not valid", oid)
		}
	}
	actor, err := encodeActor(event.Actor)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO push_events(repository_id,ref_name,old_oid,new_oid,refs_updated,actor,pushed_at) VALUES(?,?,?,?,?,?,?)`,
		event.RepositoryID, event.Ref, event.OldOID, event.NewOID, event.RefsUpdated, actor, event.PushedAt.Unix())
	if err != nil {
		return 0, err
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_events WHERE sequence <= (SELECT sequence FROM push_events ORDER BY sequence DESC LIMIT 1 OFFSET ?)`, PushEventsKept); err != nil {
		return 0, err
	}
	return sequence, tx.Commit()
}

// RecentPushes returns up to limit push events, the newest first.
func (s *Store) RecentPushes(ctx context.Context, limit int) ([]PushEvent, error) {
	return s.pushEvents(ctx, `SELECT p.sequence, p.repository_id, r.name, p.ref_name, p.old_oid, p.new_oid, p.refs_updated, p.actor, p.pushed_at
		FROM push_events p JOIN repositories r ON r.id = p.repository_id ORDER BY p.sequence DESC LIMIT ?`, limit)
}

// PushesAfter returns the push events after sequence, the oldest first.
// There are at most PushEventsKept.
func (s *Store) PushesAfter(ctx context.Context, sequence int64) ([]PushEvent, error) {
	return s.pushEvents(ctx, `SELECT p.sequence, p.repository_id, r.name, p.ref_name, p.old_oid, p.new_oid, p.refs_updated, p.actor, p.pushed_at
		FROM push_events p JOIN repositories r ON r.id = p.repository_id WHERE p.sequence > ? ORDER BY p.sequence`, sequence)
}

// LastPushSequence returns the largest sequence a push event of this
// database was ever given, or 0 before the first push. A removed push event
// keeps it.
func (s *Store) LastPushSequence(ctx context.Context) (int64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='push_events'),0)`).Scan(&sequence)
	return sequence, err
}

func (s *Store) pushEvents(ctx context.Context, query string, argument any) ([]PushEvent, error) {
	rows, err := s.db.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []PushEvent{}
	for rows.Next() {
		var event PushEvent
		var actor string
		var pushedAt int64
		if err := rows.Scan(&event.Sequence, &event.RepositoryID, &event.RepositoryName, &event.Ref, &event.OldOID, &event.NewOID, &event.RefsUpdated, &actor, &pushedAt); err != nil {
			return nil, err
		}
		if event.Actor, err = decodeActor(actor); err != nil {
			return nil, err
		}
		event.PushedAt = time.Unix(pushedAt, 0)
		events = append(events, event)
	}
	return events, rows.Err()
}
