package inventory

import (
	"context"
	"strings"
	"unicode/utf8"
)

type PublicService struct{ repo *Repository }

func NewPublicService(repo *Repository) *PublicService { return &PublicService{repo: repo} }
func (s *PublicService) List(ctx context.Context, f PublicFilter) (PublicListing, error) {
	f.Name, f.Address = strings.TrimSpace(f.Name), strings.TrimSpace(f.Address)
	f.Currency, f.Period = strings.ToUpper(strings.TrimSpace(f.Currency)), strings.TrimSpace(f.Period)
	for _, value := range []string{f.Name, f.Address, f.Currency, f.Period} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return PublicListing{}, ErrPublicFilter
		}
	}
	if utf8.RuneCountInString(f.Name) > 200 || utf8.RuneCountInString(f.Address) > 500 || utf8.RuneCountInString(f.Period) > 64 || f.Page < 1 || f.Page > 1000000 {
		return PublicListing{}, ErrPublicFilter
	}
	if f.Currency != "" && (len(f.Currency) != 3 || strings.IndexFunc(f.Currency, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0) {
		return PublicListing{}, ErrPublicFilter
	}
	if (f.Min != nil && *f.Min < 0) || (f.Max != nil && *f.Max < 0) || (f.Min != nil && f.Max != nil && *f.Min > *f.Max) {
		return PublicListing{}, ErrPublicFilter
	}
	// Amounts can only be compared within an explicit currency and period.
	if (f.Min != nil || f.Max != nil) && (f.Currency == "" || f.Period == "") {
		return PublicListing{}, ErrPublicFilter
	}
	return s.repo.PublicList(ctx, f, "")
}
func (s *PublicService) Detail(ctx context.Context, id string) (PublicListing, error) {
	return s.repo.PublicList(ctx, PublicFilter{Page: 1}, id)
}
