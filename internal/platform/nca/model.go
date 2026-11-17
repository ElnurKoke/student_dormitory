// Package nca validates attached CMS login signatures through a private NCANode
// service. It contains no account, role or application rules.
package nca

import (
	"errors"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidSignature = errors.New("invalid NCA signature or certificate")
var ErrUnavailable = errors.New("NCA verification unavailable")

const MaxSignatureLength = 32 << 10

type Identity struct{ IIN, Name, Email string }

func ValidIIN(iin string) bool {
	if len(iin) != 12 {
		return false
	}
	for _, r := range iin {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (i Identity) Validate() error {
	if !ValidIIN(i.IIN) || strings.TrimSpace(i.Name) == "" {
		return ErrInvalidSignature
	}
	for _, field := range []struct {
		value string
		max   int
	}{{i.Name, 200}, {i.Email, 254}} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.max {
			return ErrInvalidSignature
		}
		for _, r := range field.value {
			if unicode.IsControl(r) {
				return ErrInvalidSignature
			}
		}
	}
	if i.Email != "" {
		address, err := mail.ParseAddress(i.Email)
		if err != nil || address.Address != i.Email {
			return ErrInvalidSignature
		}
	}
	return nil
}
