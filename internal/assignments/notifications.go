package assignments

import (
	"context"
	"time"

	"github.com/nazacd/makonbook-api/internal/assignments/assignmentsdb"
)

// In-app notifications (spec 23.5, D39).

// maxNotifications is how many a person gets at once, newest first.
const maxNotifications = 100

// Notification is something that happened for the person.
type Notification struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Link      *string    `json:"link"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
}

// Notifications returns the person's latest notifications and how many of
// all theirs are unread.
func (s *Service) Notifications(ctx context.Context, kind string, id int32) ([]Notification, int, error) {
	q := assignmentsdb.New(s.pool)
	rows, err := q.Notifications(ctx, assignmentsdb.NotificationsParams{PersonKind: kind, PersonID: id})
	if err != nil {
		return nil, 0, err
	}
	out := make([]Notification, 0, min(len(rows), maxNotifications))
	for _, r := range rows {
		n := Notification{ID: r.ID, Kind: r.Kind, Title: r.Title, Body: r.Body, Link: textPtr(r.Link), CreatedAt: r.CreatedAt.Time.UTC()}
		if r.ReadAt.Valid {
			t := r.ReadAt.Time.UTC()
			n.ReadAt = &t
		}
		out = append(out, n)
	}
	unread, err := q.UnreadCount(ctx, assignmentsdb.UnreadCountParams{PersonKind: kind, PersonID: id})
	return out, int(unread), err
}

// MarkRead marks the person's notifications read: those of ids (others'
// are ignored), or every one. It returns how many are unread now.
func (s *Service) MarkRead(ctx context.Context, kind string, id int32, ids []int64, all bool) (int, error) {
	q := assignmentsdb.New(s.pool)
	var err error
	if all {
		err = q.MarkAllRead(ctx, assignmentsdb.MarkAllReadParams{PersonKind: kind, PersonID: id})
	} else {
		err = q.MarkRead(ctx, assignmentsdb.MarkReadParams{PersonKind: kind, PersonID: id, Ids: ids})
	}
	if err != nil {
		return 0, err
	}
	unread, err := q.UnreadCount(ctx, assignmentsdb.UnreadCountParams{PersonKind: kind, PersonID: id})
	return int(unread), err
}
