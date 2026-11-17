package applications

import (
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("application not found")
var ErrConflict = errors.New("application version or status conflict")
var ErrValidation = errors.New("invalid student fields or consent")
var errReplay = errors.New("submission already recorded")

const ConsentVersion = "test-v1"
const StudentSource = "введено студентом"

type Status string

const (
	Draft            Status = "draft"
	Submitted        Status = "submitted"
	UnderReview      Status = "under_review"
	NeedsInformation Status = "needs_information"
	Verified         Status = "verified"
	Withdrawn        Status = "withdrawn"
)

func (s Status) Label() string {
	switch s {
	case Draft:
		return "черновик"
	case Submitted:
		return "подано"
	case UnderReview:
		return "на проверке"
	case NeedsInformation:
		return "требуется дополнение"
	case Verified:
		return "проверено"
	case Withdrawn:
		return "отозвано"
	}
	return string(s)
}

type Field struct{ Value, Source, EnteredAt string }
type Fields struct{ Name, Email, Group, Request Field }
type Input struct{ Name, Email, Group, Request string }
type Profile struct {
	IIN     Field
	Fields  Fields
	Version int64
}
type Application struct {
	ID, UserID                                                           string
	Status                                                               Status
	Fields                                                               Fields
	Version                                                              int64
	SubmitKey, SubmittedAt, ConsentVersion, ConsentAcceptedAt, CreatedAt string
	SubmittedVersion                                                     int64
	Registration                                                         int64
	ReviewerID                                                           string
	ContentRevision                                                      int64
}

func (a Application) Number() string {
	if a.Registration == 0 {
		return "—"
	}
	return fmt.Sprintf("APP-%08d", a.Registration)
}
func (a Application) URL() string { return "/applications/" + a.ID }

type Change struct {
	ActorID, Action string
	Status          Status
	OccurredAt      string
	Version         int64
}
type Consent struct{ Version, Body string }

func (c Change) Label() string {
	switch c.Action {
	case "created":
		return "Черновик создан"
	case "draft_saved":
		return "Черновик сохранён"
	case "submitted":
		return "Заявление подано"
	case "withdrawn":
		return "Заявление отозвано"
	case "assigned":
		return "Назначен проверяющий"
	case "document_uploaded":
		return "Загружена версия документа"
	case "corrected":
		return "Сведения исправлены"
	case "resubmitted":
		return "Исправления отправлены"
	case "needs_information":
		return "Запрошено дополнение"
	case "review_completed":
		return "Ручная проверка завершена"
	default:
		return c.Action
	}
}

func (a Application) Editable() bool {
	return a.Status == Draft || a.Status == NeedsInformation || a.Status == Verified
}

type Document struct {
	ID, DocumentID, ApplicationID, Label, StorageKey, MediaType, SHA256, UploadedBy, UploadedAt string
	Version, ByteSize                                                                           int64
	Current                                                                                     bool
	Checked                                                                                     bool
}

func (d Document) URL() string { return "/applications/" + d.ApplicationID + "/documents/" + d.ID }

type DocumentRef struct {
	DocumentID, VersionID string
	Version               int64
}
type Review struct {
	ID, ApplicationVersion, ContentRevision int64
	ReviewerID, Result, Comment, OccurredAt string
	Fields                                  Fields
	Documents                               []DocumentRef
}
type Snapshot struct {
	ReviewerID                           string
	ContentRevision                      int64
	Version                              int64
	ActorID, OccurredAt, Comment, Result string
	Fields                               Fields
	Documents                            []DocumentRef
}
type Notification struct {
	ID                                     int64
	ApplicationID, Kind, CreatedAt, ReadAt string
}

func (n Notification) Label() string {
	switch n.Kind {
	case "registered":
		return "Заявление зарегистрировано"
	case "needs_information":
		return "Запрошено дополнение"
	case "review_completed":
		return "Ручная проверка завершена"
	}
	return n.Kind
}
func (n Notification) URL() string { return "/applications/" + n.ApplicationID }

type Reviewer struct{ ID, Name string }
type Checks struct{ Name, Email, Group, Request bool }

func (r Review) Label() string {
	if r.Result == "completed" {
		return "Ручная проверка завершена"
	}
	if r.Result == "needs_information" {
		return "Запрошено дополнение"
	}
	return r.Result
}
