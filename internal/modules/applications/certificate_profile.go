package applications

import (
	"context"
	"database/sql"
	"errors"

	"student_dormitory/internal/modules/auth"
)

const CertificateSource = "сертификат ЭЦП НУЦ РК"

// SeedCertificateProfileTx is the explicit auth-to-applications contract. Only
// empty fields and fields still sourced from a certificate are refreshed. Group
// and application snapshots remain separate from certificate identity data.
func (s *Service) SeedCertificateProfileTx(ctx context.Context, tx *sql.Tx, userID string, certificate auth.CertificateProfile) error {
	if err := s.permissions.AuthorizeTx(ctx, tx, userID, "applications.own", ""); err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return nil
		}
		return err
	}
	r := s.repo.WithTx(tx)
	p, err := r.Profile(ctx, userID)
	if err != nil {
		return err
	}
	changed := false
	seed := func(field *Field, value string) {
		if value == "" || (field.Value != "" && field.Source != CertificateSource) || (field.Value == value && field.Source == CertificateSource) {
			return
		}
		*field = Field{Value: value, Source: CertificateSource, EnteredAt: certificate.VerifiedAt}
		changed = true
	}
	seed(&p.Fields.Name, certificate.Name)
	seed(&p.Fields.Email, certificate.Email)
	if !changed {
		return nil
	}
	return r.SaveProfile(ctx, userID, p)
}
