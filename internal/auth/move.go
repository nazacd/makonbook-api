package auth

import (
	"context"
	"fmt"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
)

// MoveStudent moves a student's login data from one id to another, inside
// the sync's transaction: a self-registered student who enrols becomes the
// HolliHop student (people.Link), signed in as before with the same
// password, username and Google account (spec 21).
func MoveStudent(ctx context.Context, tx authdb.DBTX, from, to int32) error {
	q := authdb.New(tx)
	p := authdb.MoveStudentCredentialsParams{FromID: from, ToID: to}
	if err := q.MoveStudentCredentials(ctx, p); err != nil {
		return fmt.Errorf("move credentials of %d: %w", from, err)
	}
	if err := q.MoveStudentSessions(ctx, authdb.MoveStudentSessionsParams(p)); err != nil {
		return fmt.Errorf("move sessions of %d: %w", from, err)
	}
	if err := q.MoveStudentGoogle(ctx, authdb.MoveStudentGoogleParams(p)); err != nil {
		return fmt.Errorf("move the Google link of %d: %w", from, err)
	}
	return nil
}
