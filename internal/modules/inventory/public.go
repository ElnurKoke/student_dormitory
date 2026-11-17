package inventory

import (
	"errors"
	"strconv"
)

// Public models are an explicit allowlist, never embedded administrative models.
// Contacts, operators, use bases, users, bed identities and history are excluded.
type PublicPhoto struct {
	ID, Description string
	Width, Height   int
}
type PublicPrice struct {
	Minor            int64
	Currency, Period string
}
type PublicRoom struct {
	Number, Building, Floor, Equipment, Sanitary, AccessibilityNotes string
	AreaHundredths                                                   int64
	Accessible                                                       bool
	Price                                                            PublicPrice
	Free, Total                                                      int64
	Photos                                                           []PublicPhoto
}

func (r PublicRoom) Area() string {
	return strconv.FormatInt(r.AreaHundredths/100, 10) + "." + twoDigits(r.AreaHundredths%100)
}

type PublicDormitory struct {
	ID, Name, Address, Description string
	Free, Total                    int64
	Accessible                     bool
	Prices                         []PublicPrice
	Photos                         []PublicPhoto
	Rooms                          []PublicRoom
}
type PublicListing struct {
	Items              []PublicDormitory
	Total, Page, Pages int
	AsOf               string
}
type PublicFilter struct {
	Name, Address, Currency, Period string
	Min, Max                        *int64
	Free, Accessible                bool
	Page                            int
}

const PublicPageSize = 12

var ErrPublicFilter = errors.New("invalid catalogue filter")
