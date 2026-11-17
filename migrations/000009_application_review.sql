-- Local manual review proposal; no admission, queue or booking rights.
INSERT INTO auth_permissions(id,scoped) VALUES ('applications.review',0),('applications.assign',0);
INSERT INTO auth_role_permissions(role_id,permission_id) VALUES ('registry_staff','applications.review'),('administration','applications.assign');
ALTER TABLE applications ADD COLUMN reviewer_id TEXT REFERENCES auth_users(id);
ALTER TABLE applications ADD COLUMN content_revision INTEGER NOT NULL DEFAULT 1 CHECK(content_revision>0);
CREATE INDEX applications_reviewer_idx ON applications(reviewer_id,status);
CREATE TABLE application_documents (
 id TEXT PRIMARY KEY,
 application_id TEXT NOT NULL REFERENCES applications(id),
 label TEXT NOT NULL,
 UNIQUE(id,application_id)
);
CREATE TABLE application_document_versions (
 id TEXT PRIMARY KEY,
 document_id TEXT NOT NULL REFERENCES application_documents(id),
 version INTEGER NOT NULL CHECK(version>0),
 storage_key TEXT NOT NULL UNIQUE,
 media_type TEXT NOT NULL CHECK(media_type IN ('application/pdf','image/jpeg','image/png')),
 byte_size INTEGER NOT NULL CHECK(byte_size>0 AND byte_size<=10485760),
 sha256 TEXT NOT NULL,
 uploaded_by TEXT NOT NULL REFERENCES auth_users(id),
 uploaded_at TEXT NOT NULL,
 UNIQUE(document_id,version)
);
CREATE TABLE application_submissions (
 application_id TEXT NOT NULL REFERENCES applications(id),
 input_version INTEGER NOT NULL,
 submit_key TEXT NOT NULL,
 consent_version TEXT NOT NULL REFERENCES application_consents(version),
 accepted_at TEXT NOT NULL,
 PRIMARY KEY(application_id,input_version)
);
INSERT INTO application_submissions SELECT id,submitted_version,submit_key,consent_version,consent_accepted_at FROM applications WHERE submitted_at<>'';
CREATE TABLE application_history_details (
 history_id INTEGER PRIMARY KEY REFERENCES application_history(id),
 reviewer_id TEXT REFERENCES auth_users(id),
 content_revision INTEGER NOT NULL CHECK(content_revision>0),
 fields_json TEXT NOT NULL CHECK(json_valid(fields_json)),
 documents_json TEXT NOT NULL CHECK(json_valid(documents_json)),
 comment TEXT NOT NULL DEFAULT '',
 result TEXT NOT NULL DEFAULT ''
);
CREATE TABLE application_reviews (
 id INTEGER PRIMARY KEY,
 application_id TEXT NOT NULL REFERENCES applications(id),
 application_version INTEGER NOT NULL,
 content_revision INTEGER NOT NULL,
 reviewer_id TEXT NOT NULL REFERENCES auth_users(id),
 result TEXT NOT NULL CHECK(result IN ('completed','needs_information')),
 comment TEXT NOT NULL,
 fields_json TEXT NOT NULL CHECK(json_valid(fields_json)),
 documents_json TEXT NOT NULL CHECK(json_valid(documents_json)),
 occurred_at TEXT NOT NULL,
 UNIQUE(application_id,application_version)
);
CREATE TABLE application_notifications (
 id INTEGER PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES auth_users(id),
 application_id TEXT NOT NULL REFERENCES applications(id),
 kind TEXT NOT NULL CHECK(kind IN ('registered','needs_information','review_completed')),
 event_version INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 read_at TEXT NOT NULL DEFAULT '',
 UNIQUE(user_id,application_id,kind,event_version)
);
-- Existing registrations also appear once in the cabinet after upgrading.
INSERT INTO application_notifications(user_id,application_id,kind,event_version,created_at)
 SELECT user_id,id,'registered',submitted_version+1,submitted_at FROM applications WHERE submitted_at<>'';
CREATE INDEX application_notifications_user_idx ON application_notifications(user_id,id);
CREATE TRIGGER application_documents_no_update BEFORE UPDATE ON application_documents BEGIN SELECT RAISE(ABORT,'immutable document'); END;
CREATE TRIGGER application_documents_no_delete BEFORE DELETE ON application_documents BEGIN SELECT RAISE(ABORT,'immutable document'); END;
CREATE TRIGGER application_document_versions_no_update BEFORE UPDATE ON application_document_versions BEGIN SELECT RAISE(ABORT,'immutable document version'); END;
CREATE TRIGGER application_document_versions_no_delete BEFORE DELETE ON application_document_versions BEGIN SELECT RAISE(ABORT,'immutable document version'); END;
CREATE TRIGGER application_submissions_no_update BEFORE UPDATE ON application_submissions BEGIN SELECT RAISE(ABORT,'immutable submission'); END;
CREATE TRIGGER application_submissions_no_delete BEFORE DELETE ON application_submissions BEGIN SELECT RAISE(ABORT,'immutable submission'); END;
CREATE TRIGGER application_reviews_no_update BEFORE UPDATE ON application_reviews BEGIN SELECT RAISE(ABORT,'immutable review'); END;
CREATE TRIGGER application_reviews_no_delete BEFORE DELETE ON application_reviews BEGIN SELECT RAISE(ABORT,'immutable review'); END;
CREATE TRIGGER application_history_details_no_update BEFORE UPDATE ON application_history_details BEGIN SELECT RAISE(ABORT,'immutable history'); END;
CREATE TRIGGER application_history_details_no_delete BEFORE DELETE ON application_history_details BEGIN SELECT RAISE(ABORT,'immutable history'); END;
