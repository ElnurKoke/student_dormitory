package applications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"student_dormitory/internal/platform/database"
)

func (r *Repository) Exclusive(ctx context.Context, fn func(*Repository, *sql.Tx) error) error {
	return database.WithinTx(ctx, r.db, func(ctx context.Context, tx *sql.Tx) error { return fn(r.WithTx(tx), tx) })
}
func (r *Repository) Assigned(ctx context.Context, actor, id string) (Application, error) {
	return scan(r.q.QueryRowContext(ctx, `SELECT `+columns+` FROM applications a LEFT JOIN application_registrations r ON r.application_id=a.id WHERE a.reviewer_id=? AND a.id=?`, actor, id))
}
func (r *Repository) AssignmentTarget(ctx context.Context, id string) (Application, error) {
	return scan(r.q.QueryRowContext(ctx, `SELECT `+columns+` FROM applications a LEFT JOIN application_registrations r ON r.application_id=a.id WHERE a.id=? AND a.status IN ('submitted','under_review','needs_information')`, id))
}
func (r *Repository) StaffList(ctx context.Context, actor string, assign bool) ([]Application, error) {
	filter := `a.reviewer_id=?`
	args := []any{actor}
	if assign {
		filter = `a.status IN ('submitted','under_review','needs_information')`
		args = nil
	}
	// Coordinators get routing metadata only, never fields or file access.
	if assign {
		rows, err := r.q.QueryContext(ctx, `SELECT a.id,a.status,a.version,a.submitted_at,COALESCE(a.reviewer_id,''),r.number FROM applications a JOIN application_registrations r ON r.application_id=a.id WHERE `+filter+` ORDER BY a.submitted_at,a.id`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Application
		for rows.Next() {
			var a Application
			if err := rows.Scan(&a.ID, &a.Status, &a.Version, &a.SubmittedAt, &a.ReviewerID, &a.Registration); err != nil {
				return nil, err
			}
			out = append(out, a)
		}
		return out, rows.Err()
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+columns+` FROM applications a LEFT JOIN application_registrations r ON r.application_id=a.id WHERE `+filter+` ORDER BY a.submitted_at,a.id`, args...)
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
func (r *Repository) Documents(ctx context.Context, id string) ([]Document, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT v.id,d.id,d.application_id,d.label,v.storage_key,v.media_type,v.byte_size,v.sha256,v.uploaded_by,v.uploaded_at,v.version, v.version=(SELECT MAX(v2.version) FROM application_document_versions v2 WHERE v2.document_id=d.id) FROM application_documents d JOIN application_document_versions v ON v.document_id=d.id WHERE d.application_id=? ORDER BY d.id,v.version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.DocumentID, &d.ApplicationID, &d.Label, &d.StorageKey, &d.MediaType, &d.ByteSize, &d.SHA256, &d.UploadedBy, &d.UploadedAt, &d.Version, &d.Current); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func refs(docs []Document) []DocumentRef {
	out := []DocumentRef{}
	for _, d := range docs {
		if d.Current {
			out = append(out, DocumentRef{d.DocumentID, d.ID, d.Version})
		}
	}
	return out
}
func (r *Repository) AddHistoryAs(ctx context.Context, a Application, actor, action, at, comment, result string) error {
	docs, err := r.Documents(ctx, a.ID)
	if err != nil {
		return err
	}
	fields, err := json.Marshal(a.Fields)
	if err != nil {
		return err
	}
	documents, err := json.Marshal(refs(docs))
	if err != nil {
		return err
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO application_history(application_id,actor_id,action,status,occurred_at,version) VALUES (?,?,?,?,?,?)`, a.ID, actor, action, a.Status, at, a.Version)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO application_history_details(history_id,reviewer_id,content_revision,fields_json,documents_json,comment,result) VALUES (?,NULLIF(?,''),?,?,?,?,?)`, id, a.ReviewerID, a.ContentRevision, string(fields), string(documents), comment, result)
	return err
}
func (r *Repository) Snapshots(ctx context.Context, id string) ([]Snapshot, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT h.version,h.actor_id,h.occurred_at,COALESCE(d.reviewer_id,''),d.content_revision,d.fields_json,d.documents_json,d.comment,d.result FROM application_history h JOIN application_history_details d ON d.history_id=h.id WHERE h.application_id=? ORDER BY h.version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var s Snapshot
		var f, d string
		if err := rows.Scan(&s.Version, &s.ActorID, &s.OccurredAt, &s.ReviewerID, &s.ContentRevision, &f, &d, &s.Comment, &s.Result); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(f), &s.Fields); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(d), &s.Documents); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (r *Repository) InsertReview(ctx context.Context, a Application, actor, result, comment, at string) error {
	docs, err := r.Documents(ctx, a.ID)
	if err != nil {
		return err
	}
	f, err := json.Marshal(a.Fields)
	if err != nil {
		return err
	}
	d, err := json.Marshal(refs(docs))
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO application_reviews(application_id,application_version,content_revision,reviewer_id,result,comment,fields_json,documents_json,occurred_at) VALUES (?,?,?,?,?,?,?,?,?)`, a.ID, a.Version, a.ContentRevision, actor, result, comment, string(f), string(d), at)
	return err
}
func (r *Repository) Reviews(ctx context.Context, id string) ([]Review, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id,application_version,content_revision,reviewer_id,result,comment,fields_json,documents_json,occurred_at FROM application_reviews WHERE application_id=? ORDER BY application_version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var v Review
		var f, d string
		if err := rows.Scan(&v.ID, &v.ApplicationVersion, &v.ContentRevision, &v.ReviewerID, &v.Result, &v.Comment, &f, &d, &v.OccurredAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(f), &v.Fields); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(d), &v.Documents); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *Repository) Submission(ctx context.Context, id string, version int64) (string, string, error) {
	var key, consent string
	err := r.q.QueryRowContext(ctx, `SELECT submit_key,consent_version FROM application_submissions WHERE application_id=? AND input_version=?`, id, version).Scan(&key, &consent)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return key, consent, err
}
func (r *Repository) InsertSubmission(ctx context.Context, a Application, version int64) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO application_submissions(application_id,input_version,submit_key,consent_version,accepted_at) VALUES (?,?,?,?,?)`, a.ID, version, a.SubmitKey, a.ConsentVersion, a.ConsentAcceptedAt)
	return err
}
func (r *Repository) Notify(ctx context.Context, a Application, kind, at string) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO application_notifications(user_id,application_id,kind,event_version,created_at) VALUES (?,?,?,?,?) ON CONFLICT(user_id,application_id,kind,event_version) DO NOTHING`, a.UserID, a.ID, kind, a.Version, at)
	return err
}
func (r *Repository) Notifications(ctx context.Context, actor string) ([]Notification, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id,application_id,kind,created_at,read_at FROM application_notifications WHERE user_id=? ORDER BY id DESC`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.ApplicationID, &n.Kind, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (r *Repository) ReadNotification(ctx context.Context, actor string, id int64, at string) error {
	var read string
	err := r.q.QueryRowContext(ctx, `SELECT read_at FROM application_notifications WHERE user_id=? AND id=?`, actor, id).Scan(&read)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if read != "" {
		return errReplay
	}
	res, err := r.q.ExecContext(ctx, `UPDATE application_notifications SET read_at=? WHERE user_id=? AND id=? AND read_at=''`, at, actor, id)
	return changed(res, err)
}
func (r *Repository) InsertDocument(ctx context.Context, d Document, create bool) error {
	if create {
		if _, err := r.q.ExecContext(ctx, `INSERT INTO application_documents(id,application_id,label) VALUES (?,?,?)`, d.DocumentID, d.ApplicationID, d.Label); err != nil {
			return err
		}
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO application_document_versions(id,document_id,version,storage_key,media_type,byte_size,sha256,uploaded_by,uploaded_at) VALUES (?,?,?,?,?,?,?,?,?)`, d.ID, d.DocumentID, d.Version, d.StorageKey, d.MediaType, d.ByteSize, d.SHA256, d.UploadedBy, d.UploadedAt)
	return err
}
