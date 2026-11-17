package applications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type Repository struct {
	q  queryer
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository          { return &Repository{q: db, db: db} }
func (r *Repository) WithTx(tx *sql.Tx) *Repository { return &Repository{q: tx, db: r.db} }
func (r *Repository) Profile(ctx context.Context, actor string) (Profile, error) {
	var p Profile
	var data string
	err := r.q.QueryRowContext(ctx, "SELECT fields_json,version FROM application_profiles WHERE user_id=?", actor).Scan(&data, &p.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(data), &p.Fields)
	return p, err
}
func (r *Repository) SaveProfile(ctx context.Context, actor string, p Profile) error {
	data, err := json.Marshal(p.Fields)
	if err != nil {
		return err
	}
	if p.Version == 0 {
		_, err = r.q.ExecContext(ctx, "INSERT INTO application_profiles(user_id,fields_json,version) VALUES (?,?,1)", actor, string(data))
		return err
	}
	res, err := r.q.ExecContext(ctx, "UPDATE application_profiles SET fields_json=?,version=version+1 WHERE user_id=? AND version=?", string(data), actor, p.Version)
	return changed(res, err)
}

const columns = `a.id,a.user_id,a.status,a.fields_json,a.version,a.submit_key,COALESCE(a.submitted_version,0),a.submitted_at,COALESCE(a.consent_version,''),a.consent_accepted_at,a.created_at,COALESCE(r.number,0),COALESCE(a.reviewer_id,''),a.content_revision`

func scan(row interface{ Scan(...any) error }) (Application, error) {
	var a Application
	var data string
	err := row.Scan(&a.ID, &a.UserID, &a.Status, &data, &a.Version, &a.SubmitKey, &a.SubmittedVersion, &a.SubmittedAt, &a.ConsentVersion, &a.ConsentAcceptedAt, &a.CreatedAt, &a.Registration, &a.ReviewerID, &a.ContentRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal([]byte(data), &a.Fields)
	return a, err
}
func (r *Repository) Get(ctx context.Context, actor, id string) (Application, error) {
	return scan(r.q.QueryRowContext(ctx, `SELECT `+columns+` FROM applications a LEFT JOIN application_registrations r ON r.application_id=a.id WHERE a.user_id=? AND a.id=?`, actor, id))
}
func (r *Repository) List(ctx context.Context, actor string) ([]Application, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+columns+` FROM applications a LEFT JOIN application_registrations r ON r.application_id=a.id WHERE a.user_id=? ORDER BY a.created_at DESC,a.id`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Application
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (r *Repository) Insert(ctx context.Context, a Application) error {
	data, err := json.Marshal(a.Fields)
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO applications(id,user_id,status,fields_json,version,submit_key,created_at) VALUES (?,?,'draft',?,1,?,?)`, a.ID, a.UserID, string(data), a.SubmitKey, a.CreatedAt)
	return err
}
func (r *Repository) Update(ctx context.Context, a Application, previous int64) error {
	data, err := json.Marshal(a.Fields)
	if err != nil {
		return err
	}
	res, err := r.q.ExecContext(ctx, `UPDATE applications SET fields_json=?,status=?,version=?,submitted_version=NULLIF(?,0),submitted_at=?,consent_version=NULLIF(?,''),consent_accepted_at=?,reviewer_id=NULLIF(?,''),content_revision=? WHERE id=? AND user_id=? AND version=?`, string(data), a.Status, a.Version, a.SubmittedVersion, a.SubmittedAt, a.ConsentVersion, a.ConsentAcceptedAt, a.ReviewerID, a.ContentRevision, a.ID, a.UserID, previous)
	return changed(res, err)
}
func changed(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrConflict
	}
	return err
}
func (r *Repository) Register(ctx context.Context, id string) error {
	_, err := r.q.ExecContext(ctx, "INSERT INTO application_registrations(application_id) VALUES (?)", id)
	return err
}
func (r *Repository) Consent(ctx context.Context, version string) (Consent, error) {
	var c Consent
	err := r.q.QueryRowContext(ctx, "SELECT version,body FROM application_consents WHERE version=?", version).Scan(&c.Version, &c.Body)
	return c, err
}
func (r *Repository) AddHistory(ctx context.Context, a Application, action, at string) error {
	return r.AddHistoryAs(ctx, a, a.UserID, action, at, "", "")
}
func (r *Repository) History(ctx context.Context, actor, id string) ([]Change, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT h.actor_id,h.action,h.status,h.occurred_at,h.version FROM application_history h JOIN applications a ON a.id=h.application_id WHERE a.user_id=? AND a.id=? ORDER BY h.version`, actor, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.ActorID, &c.Action, &c.Status, &c.OccurredAt, &c.Version); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
