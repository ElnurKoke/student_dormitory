package applications

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
)

type Permissions interface {
	Authorize(context.Context, string, string, string) error
	AuthorizeTx(context.Context, *sql.Tx, string, string, string) error
}

// CertificateIdentities supplies only the current user's verified certificate
// through the auth module contract; applications never reads auth tables.
type CertificateIdentities interface {
	CertificateProfile(context.Context, string) (*auth.CertificateProfile, error)
}

type Service struct {
	repo        *Repository
	permissions Permissions
	audit       *audit.Recorder
	identities  CertificateIdentities
}

func NewService(r *Repository, p Permissions, a *audit.Recorder, identities CertificateIdentities) *Service {
	return &Service{repo: r, permissions: p, audit: a, identities: identities}
}
func (s *Service) authorize(ctx context.Context, actor string) error {
	return s.permissions.Authorize(ctx, actor, "applications.own", "")
}
func (s *Service) Profile(ctx context.Context, actor string) (Profile, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return Profile{}, err
	}
	profile, err := s.repo.Profile(ctx, actor)
	if err != nil {
		return Profile{}, err
	}
	certificate, err := s.identities.CertificateProfile(ctx, actor)
	if err != nil {
		return Profile{}, err
	}
	if certificate != nil {
		profile.IIN = Field{Value: certificate.IIN, Source: CertificateSource, EnteredAt: certificate.VerifiedAt}
	}
	return profile, nil
}
func (s *Service) List(ctx context.Context, actor string) ([]Application, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, actor)
}
func (s *Service) Detail(ctx context.Context, actor, id string) (Application, []Change, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return Application{}, nil, err
	}
	a, err := s.repo.Get(ctx, actor, id)
	if err != nil {
		return a, nil, err
	}
	h, err := s.repo.History(ctx, actor, id)
	return a, h, err
}
func (s *Service) Consent(ctx context.Context, actor, version string) (Consent, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return Consent{}, err
	}
	if version == "" {
		version = ConsentVersion
	}
	return s.repo.Consent(ctx, version)
}
func newID() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func field(v, at string) Field {
	return Field{Value: strings.TrimSpace(v), Source: StudentSource, EnteredAt: at}
}
func fields(in Input, old Fields, at string) Fields {
	keep := func(v string, previous Field) Field {
		if strings.TrimSpace(v) == previous.Value && previous.Source != "" {
			return previous
		}
		return field(v, at)
	}
	return Fields{keep(in.Name, old.Name), keep(in.Email, old.Email), keep(in.Group, old.Group), keep(in.Request, old.Request)}
}
func validate(in Input, required bool) error {
	for _, v := range []struct {
		s   string
		max int
	}{{in.Name, 200}, {in.Email, 254}, {in.Group, 100}, {in.Request, 2000}} {
		if !utf8.ValidString(v.s) || utf8.RuneCountInString(v.s) > v.max || strings.ContainsRune(v.s, 0) {
			return ErrValidation
		}
	}
	if required && (strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Email) == "" || strings.TrimSpace(in.Group) == "" || strings.TrimSpace(in.Request) == "") {
		return ErrValidation
	}
	if in.Email != "" {
		m, err := mail.ParseAddress(strings.TrimSpace(in.Email))
		if err != nil || m.Address != strings.TrimSpace(in.Email) {
			return ErrValidation
		}
	}
	return nil
}
func input(f Fields) Input { return Input{f.Name.Value, f.Email.Value, f.Group.Value, f.Request.Value} }
func event(actor, object, id, action string) audit.Event {
	return audit.Event{ActorID: actor, ObjectType: object, ObjectID: id, Action: "applications." + action, Result: "success"}
}
func (s *Service) change(ctx context.Context, actor string, fn func(*Repository) (audit.Event, error)) error {
	return s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, "applications.own", ""); err != nil {
			return audit.Event{}, err
		}
		return fn(s.repo.WithTx(tx))
	})
}
func (s *Service) SaveProfile(ctx context.Context, actor string, in Input, version int64) error {
	if err := s.authorize(ctx, actor); err != nil {
		return err
	}
	in.Request = ""
	if err := validate(in, false); err != nil {
		return err
	}
	return s.change(ctx, actor, func(r *Repository) (audit.Event, error) {
		p, err := r.Profile(ctx, actor)
		if err != nil {
			return audit.Event{}, err
		}
		if p.Version != version {
			return audit.Event{}, ErrConflict
		}
		p.Fields = fields(in, p.Fields, timestamp())
		if err := r.SaveProfile(ctx, actor, p); err != nil {
			return audit.Event{}, err
		}
		return event(actor, "student_profile", actor, "profile_saved"), nil
	})
}
func (s *Service) Create(ctx context.Context, actor string) (Application, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return Application{}, err
	}
	id, err := newID()
	if err != nil {
		return Application{}, err
	}
	key, err := newID()
	if err != nil {
		return Application{}, err
	}
	a := Application{ContentRevision: 1, ID: id, UserID: actor, Status: Draft, Version: 1, SubmitKey: key, CreatedAt: timestamp()}
	err = s.change(ctx, actor, func(r *Repository) (audit.Event, error) {
		p, err := r.Profile(ctx, actor)
		if err != nil {
			return audit.Event{}, err
		}
		a.Fields = p.Fields
		if err := r.Insert(ctx, a); err != nil {
			return audit.Event{}, err
		}
		if err := r.AddHistory(ctx, a, "created", a.CreatedAt); err != nil {
			return audit.Event{}, err
		}
		return event(actor, "application", id, "created"), nil
	})
	return a, err
}
func (s *Service) Edit(ctx context.Context, actor, id string, in Input, version int64) error {
	if err := s.authorize(ctx, actor); err != nil {
		return err
	}
	if err := validate(in, false); err != nil {
		return err
	}
	return s.change(ctx, actor, func(r *Repository) (audit.Event, error) {
		a, err := r.Get(ctx, actor, id)
		if err != nil {
			return audit.Event{}, err
		}
		if !a.Editable() || a.Version != version {
			return audit.Event{}, ErrConflict
		}
		at := timestamp()
		updated := fields(in, a.Fields, at)
		action := "draft_saved"
		if a.Status != Draft {
			action = "corrected"
		}
		if updated != a.Fields {
			a.ContentRevision++
			if a.Status == Verified {
				a.Status = NeedsInformation
			}
		}
		a.Fields = updated
		a.Version++
		if err := r.Update(ctx, a, version); err != nil {
			return audit.Event{}, err
		}
		if err := r.AddHistory(ctx, a, action, at); err != nil {
			return audit.Event{}, err
		}
		return event(actor, "application", id, action), nil
	})
}
func (s *Service) Submit(ctx context.Context, actor, id, key, consent string, accepted bool, version int64) (Application, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return Application{}, err
	}
	var a Application
	err := s.change(ctx, actor, func(r *Repository) (audit.Event, error) {
		var err error
		a, err = r.Get(ctx, actor, id)
		if err != nil {
			return audit.Event{}, err
		}
		if key == "" || key != a.SubmitKey {
			return audit.Event{}, ErrConflict
		}
		if !accepted {
			return audit.Event{}, ErrValidation
		}
		previousKey, previousConsent, err := r.Submission(ctx, id, version)
		if err != nil {
			return audit.Event{}, err
		}
		if previousKey != "" {
			if previousKey == key && previousConsent == consent {
				return audit.Event{}, errReplay
			}
			return audit.Event{}, ErrConflict
		}
		if (a.Status != Draft && a.Status != NeedsInformation) || a.Version != version {
			return audit.Event{}, ErrConflict
		}
		if consent != ConsentVersion {
			return audit.Event{}, ErrValidation
		}
		if err := validate(input(a.Fields), true); err != nil {
			return audit.Event{}, err
		}
		if _, err := r.Consent(ctx, ConsentVersion); err != nil {
			return audit.Event{}, err
		}
		initial := a.SubmittedAt == ""
		at := timestamp()
		if initial {
			a.SubmittedVersion = version
			a.SubmittedAt = at
		}
		a.Version++
		a.Status = Submitted
		if a.ReviewerID != "" {
			a.Status = UnderReview
		}
		a.ConsentVersion = consent
		a.ConsentAcceptedAt = at
		if err := r.Update(ctx, a, version); err != nil {
			return audit.Event{}, err
		}
		if initial {
			if err := r.Register(ctx, id); err != nil {
				return audit.Event{}, err
			}
		}
		if err := r.InsertSubmission(ctx, a, version); err != nil {
			return audit.Event{}, err
		}
		action := "resubmitted"
		if initial {
			action = "submitted"
		}
		if err := r.AddHistory(ctx, a, action, at); err != nil {
			return audit.Event{}, err
		}
		if initial {
			if err := r.Notify(ctx, a, "registered", at); err != nil {
				return audit.Event{}, err
			}
		}
		a, err = r.Get(ctx, actor, id)
		return event(actor, "application", id, action), err
	})
	if errors.Is(err, errReplay) {
		err = nil
	}
	return a, err
}

// Local proposed rule: only a submitted application can be withdrawn, before review.
func (s *Service) Withdraw(ctx context.Context, actor, id string, version int64) error {
	if err := s.authorize(ctx, actor); err != nil {
		return err
	}
	return s.change(ctx, actor, func(r *Repository) (audit.Event, error) {
		a, err := r.Get(ctx, actor, id)
		if err != nil {
			return audit.Event{}, err
		}
		if a.Status != Submitted || a.Version != version {
			return audit.Event{}, ErrConflict
		}
		a.Status = Withdrawn
		a.Version++
		at := timestamp()
		if err := r.Update(ctx, a, version); err != nil {
			return audit.Event{}, err
		}
		if err := r.AddHistory(ctx, a, "withdrawn", at); err != nil {
			return audit.Event{}, err
		}
		return event(actor, "application", id, "withdrawn"), nil
	})
}
