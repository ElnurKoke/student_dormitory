package applications

import (
	"context"
	"database/sql"
	"testing"

	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/nca"
)

func TestCertificateProfileProvenanceSurvivesEditingAndSnapshot(t *testing.T) {
	s, db, ctx := fixture(t)
	certificate := auth.CertificateProfile{Identity: nca.Identity{IIN: "000000000001", Name: "ФИО сертификата", Email: "certificate@example.test"}, VerifiedAt: "2026-11-16T01:02:03Z"}
	seed := func(actor string) {
		t.Helper()
		if err := database.WithinTx(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
			return s.SeedCertificateProfileTx(ctx, tx, actor, certificate)
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("one")
	p, err := s.Profile(ctx, "one")
	if err != nil || p.Fields.Name.Source != CertificateSource || p.Fields.Email.Source != CertificateSource || p.Fields.Group.Value != "" || p.IIN.Value != "" {
		t.Fatal(p, err)
	}
	seed("one")
	p2, _ := s.Profile(ctx, "one")
	if p2.Version != p.Version {
		t.Fatal("identical login changed student profile version")
	}
	in := Input{Name: certificate.Name, Email: certificate.Email, Group: "Группа 1"}
	if err := s.SaveProfile(ctx, "one", in, p.Version); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Profile(ctx, "one")
	if p.Fields.Name.Source != CertificateSource || p.Fields.Name.EnteredAt != certificate.VerifiedAt || p.Fields.Email.Source != CertificateSource || p.Fields.Group.Source != StudentSource {
		t.Fatal("saving group erased certificate source", p)
	}
	a, err := s.Create(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if a.Fields.Name.Source != CertificateSource || a.Fields.Name.Value != certificate.Name {
		t.Fatal("draft did not copy source", a.Fields)
	}
	in.Name = "Имя, изменённое студентом"
	if err := s.SaveProfile(ctx, "one", in, p.Version); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Profile(ctx, "one")
	if p.Fields.Name.Source != StudentSource {
		t.Fatal("manual change retained certificate claim", p)
	}
	certificate.Name = "Новое ФИО сертификата"
	certificate.Email = "renewed@example.test"
	certificate.VerifiedAt = "2026-11-16T02:03:04Z"
	seed("one")
	p, _ = s.Profile(ctx, "one")
	if p.Fields.Name.Value != in.Name || p.Fields.Email.Value != certificate.Email || p.Fields.Email.Source != CertificateSource || p.Fields.Group.Value != "Группа 1" {
		t.Fatal("renewal overwrote manual fields", p)
	}
	a, _, err = s.Detail(ctx, "one", a.ID)
	if err != nil || a.Fields.Name.Value != "ФИО сертификата" || a.Fields.Email.Value != "certificate@example.test" {
		t.Fatal("login changed historical application snapshot", a, err)
	}
	seed("admin")
	if count(t, db, `SELECT count(*) FROM application_profiles WHERE user_id='admin'`) != 0 {
		t.Fatal("non-student got student profile")
	}
}
