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
// events, in one transaction. Push events are machine-local: backups do not
// carry them.
func (s *Store) RecordPush(ctx context.Context, event PushEvent) error {
	if event.RefsUpdated < 1 || event.Ref == "" || event.PushedAt.IsZero() {
		return errors.New("a push event needs a ref, at least one updated ref and a time")
	}
	for _, oid := range []string{event.OldOID, event.NewOID} {
		if oid != "" && !validObjectID(oid) {
			return fmt.Errorf("push event object ID %q is not valid", oid)
		}
	}
	actor, err := encodeActor(event.Actor)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO push_events(repository_id,ref_name,old_oid,new_oid,refs_updated,actor,pushed_at) VALUES(?,?,?,?,?,?,?)`,
		event.RepositoryID, event.Ref, event.OldOID, event.NewOID, event.RefsUpdated, actor, event.PushedAt.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_events WHERE sequence <= (SELECT sequence FROM push_events ORDER BY sequence DESC LIMIT 1 OFFSET ?)`, PushEventsKept); err != nil {
		return err
	}
	return tx.Commit()
}

// RecentPushes returns up to limit push events, the newest first.
func (s *Store) RecentPushes(ctx context.Context, limit int) ([]PushEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.repository_id, r.name, p.ref_name, p.old_oid, p.new_oid, p.refs_updated, p.actor, p.pushed_at
		FROM push_events p JOIN repositories r ON r.id = p.repository_id ORDER BY p.sequence DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []PushEvent{}
	for rows.Next() {
		var event PushEvent
		var actor string
		var pushedAt int64
		if err := rows.Scan(&event.RepositoryID, &event.RepositoryName, &event.Ref, &event.OldOID, &event.NewOID, &event.RefsUpdated, &actor, &pushedAt); err != nil {
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
