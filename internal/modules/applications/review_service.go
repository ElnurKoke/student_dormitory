package applications

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
)

type ReviewerDirectory interface {
	UsersWithPermission(context.Context, string) ([]auth.User, error)
}
type ReviewService struct {
	s         *Service
	directory ReviewerDirectory
}

func NewReviewService(s *Service, d ReviewerDirectory) *ReviewService { return &ReviewService{s, d} }
func (s *ReviewService) Assignments(ctx context.Context, actor string) ([]Application, []Reviewer, error) {
	if err := s.s.permissions.Authorize(ctx, actor, "applications.assign", ""); err != nil {
		return nil, nil, err
	}
	list, err := s.s.repo.StaffList(ctx, actor, true)
	if err != nil {
		return nil, nil, err
	}
	users, err := s.directory.UsersWithPermission(ctx, "applications.review")
	if err != nil {
		return nil, nil, err
	}
	var candidates []Reviewer
	for _, u := range users {
		candidates = append(candidates, Reviewer{u.ID, u.DisplayName})
	}
	return list, candidates, nil
}
func (s *ReviewService) List(ctx context.Context, actor string) ([]Application, error) {
	if err := s.s.permissions.Authorize(ctx, actor, "applications.review", ""); err != nil {
		return nil, err
	}
	return s.s.repo.StaffList(ctx, actor, false)
}
func (s *ReviewService) Assign(ctx context.Context, actor, id, reviewer string, version int64) error {
	return s.s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.s.permissions.AuthorizeTx(ctx, tx, actor, "applications.assign", ""); err != nil {
			return audit.Event{}, err
		}
		if err := s.s.permissions.AuthorizeTx(ctx, tx, reviewer, "applications.review", ""); err != nil {
			return audit.Event{}, err
		}
		r := s.s.repo.WithTx(tx)
		a, err := r.AssignmentTarget(ctx, id)
		if err != nil {
			return audit.Event{}, err
		}
		if a.Version != version || a.ReviewerID == reviewer {
			return audit.Event{}, ErrConflict
		}
		a.ReviewerID = reviewer
		if a.Status == Submitted {
			a.Status = UnderReview
		}
		a.Version++
		at := timestamp()
		if err := r.Update(ctx, a, version); err != nil {
			return audit.Event{}, err
		}
		if err := r.AddHistoryAs(ctx, a, actor, "assigned", at, "", ""); err != nil {
			return audit.Event{}, err
		}
		return event(actor, "application", id, "assigned"), nil
	})
}

// Workspace is only constructed after checking ownership or current assignment.
type Workspace struct {
	Application Application
	Consent     Consent
	History     []Change
	Documents   []Document
	Reviews     []Review
	Snapshots   []Snapshot
	Checks      Checks
}

func (s *Service) Workspace(ctx context.Context, actor, id string, staff bool) (Workspace, error) {
	var out Workspace
	err := s.repo.Exclusive(ctx, func(r *Repository, tx *sql.Tx) error {
		permission := "applications.own"
		if staff {
			permission = "applications.review"
		}
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, permission, ""); err != nil {
			return err
		}
		var a Application
		var err error
		if staff {
			a, err = r.Assigned(ctx, actor, id)
		} else {
			a, err = r.Get(ctx, actor, id)
		}
		if err != nil {
			return err
		}
		out.Application = a
		consentVersion := a.ConsentVersion
		if consentVersion == "" || (!staff && a.Status == NeedsInformation) {
			consentVersion = ConsentVersion
		}
		out.Consent, err = r.Consent(ctx, consentVersion)
		if err != nil {
			return err
		}
		out.History, err = r.History(ctx, a.UserID, id)
		if err != nil {
			return err
		}
		out.Documents, err = r.Documents(ctx, id)
		if err != nil {
			return err
		}
		out.Reviews, err = r.Reviews(ctx, id)
		if err != nil {
			return err
		}
		out.Snapshots, err = r.Snapshots(ctx, id)
		if err != nil {
			return err
		}
		for _, review := range out.Reviews {
			if review.Result != "completed" {
				continue
			}
			out.Checks = Checks{a.Fields.Name == review.Fields.Name, a.Fields.Email == review.Fields.Email, a.Fields.Group == review.Fields.Group, a.Fields.Request == review.Fields.Request}
			for i, d := range out.Documents {
				for _, ref := range review.Documents {
					if d.Current && ref.VersionID == d.ID {
						out.Documents[i].Checked = true
					}
				}
			}
			break
		}
		return nil
	})
	return out, err
}
func (s *ReviewService) Review(ctx context.Context, actor, id, result, comment string, confirmed bool, version int64) error {
	if err := s.s.permissions.Authorize(ctx, actor, "applications.review", ""); err != nil {
		return err
	}
	comment = strings.TrimSpace(comment)
	if (result != "completed" && result != "needs_information") || !utf8.ValidString(comment) || comment == "" || utf8.RuneCountInString(comment) > 2000 || strings.ContainsRune(comment, 0) || (result == "completed" && !confirmed) {
		return ErrValidation
	}
	return s.s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.s.permissions.AuthorizeTx(ctx, tx, actor, "applications.review", ""); err != nil {
			return audit.Event{}, err
		}
		r := s.s.repo.WithTx(tx)
		a, err := r.Assigned(ctx, actor, id)
		if err != nil {
			return audit.Event{}, err
		}
		if a.Status != UnderReview || a.Version != version {
			return audit.Event{}, ErrConflict
		}
		if err := validate(input(a.Fields), true); err != nil {
			return audit.Event{}, err
		}
		a.Version++
		action := "needs_information"
		at := timestamp()
		a.Status = NeedsInformation
		if result == "completed" {
			a.Status = Verified
			action = "review_completed"
		}
		if err := r.Update(ctx, a, version); err != nil {
			return audit.Event{}, err
		}
		if err := r.InsertReview(ctx, a, actor, result, comment, at); err != nil {
			return audit.Event{}, err
		}
		if err := r.AddHistoryAs(ctx, a, actor, action, at, comment, result); err != nil {
			return audit.Event{}, err
		}
		if err := r.Notify(ctx, a, action, at); err != nil {
			return audit.Event{}, err
		}
		return event(actor, "application", id, action), nil
	})
}
func (s *Service) Notifications(ctx context.Context, actor string) ([]Notification, error) {
	if err := s.permissions.Authorize(ctx, actor, "account.read", ""); err != nil {
		return nil, err
	}
	return s.repo.Notifications(ctx, actor)
}
func (s *Service) ReadNotification(ctx context.Context, actor string, id int64) error {
	err := s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, "account.read", ""); err != nil {
			return audit.Event{}, err
		}
		if err := s.repo.WithTx(tx).ReadNotification(ctx, actor, id, timestamp()); err != nil {
			return audit.Event{}, err
		}
		return event(actor, "application_notification", strconv.FormatInt(id, 10), "notification_read"), nil
	})
	if errors.Is(err, errReplay) {
		return nil
	}
	return err
}
