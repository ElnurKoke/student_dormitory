package inventory

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

func validate(kind Kind, d Draft) (Fields, error) {
	errors := ValidationError{}
	text := func(key, value string, max int, required bool) string {
		value = strings.TrimSpace(value)
		if required && value == "" {
			errors[key] = "Поле «" + fieldLabel(key) + "» обязательно."
		}
		if utf8.RuneCountInString(value) > max {
			errors[key] = "Слишком длинное значение. Допустимо до " + strconv.Itoa(max) + " символов."
		}
		return value
	}
	f := Fields{}
	switch kind {
	case Dormitory:
		f.Name = text("name", d.Name, 200, true)
		f.Address = text("address", d.Address, 500, true)
		f.Operator = text("operator", d.Operator, 300, true)
		f.UseBasis = text("use_basis", d.UseBasis, 500, true)
		f.Contacts = text("contacts", d.Contacts, 1000, true)
		f.Description = text("description", d.Description, 2000, false)
		f.Published = d.Published
	case Building:
		f.Name = text("name", d.Name, 200, true)
	case Floor:
		f.Number = text("number", d.Number, 64, true)
	case Bed:
		f.Number = text("number", d.Number, 64, true)
	case Room:
		f.Number = text("number", d.Number, 64, true)
		var err error
		f.Capacity, err = parseInteger(d.Capacity)
		if err != nil || f.Capacity < 1 {
			errors["capacity"] = "Вместимость должна быть целым числом больше нуля."
		}
		f.AreaHundredths, err = parseArea(d.Area)
		if err != nil || f.AreaHundredths < 1 {
			errors["area"] = "Укажите положительную площадь с точностью до 0,01 м²."
		}
		f.PriceMinor, err = parseInteger(d.PriceMinor)
		if err != nil || f.PriceMinor < 0 {
			errors["price_minor"] = "Укажите целую неотрицательную сумму в минимальных денежных единицах."
		}
		f.Currency = strings.ToUpper(text("currency", d.Currency, 3, true))
		if len(f.Currency) != 3 || strings.IndexFunc(f.Currency, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
			errors["currency"] = "Введите код валюты из трёх латинских букв, например KZT."
		}
		f.PricePeriod = text("price_period", d.PricePeriod, 64, true)
		f.Equipment = text("equipment", d.Equipment, 1000, true)
		f.Sanitary = text("sanitary", d.Sanitary, 1000, true)
		f.Accessible = d.Accessible
		f.AccessibilityNotes = text("accessibility_notes", d.AccessibilityNotes, 1000, false)
	default:
		return Fields{}, ErrNotFound
	}
	if len(errors) > 0 {
		return Fields{}, errors
	}
	return f, nil
}

func fieldLabel(key string) string {
	return map[string]string{"name": "Название", "number": "Номер", "address": "Адрес", "operator": "Собственник или оператор", "use_basis": "Основание использования", "contacts": "Контакты", "currency": "Валюта", "price_period": "Период стоимости", "equipment": "Оснащение", "sanitary": "Санитарно-бытовые условия"}[key]
}

func parseInteger(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseInt(s, 10, 64)
}
func parseArea(s string) (int64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	whole, part, has := strings.Cut(s, ".")
	n, err := parseInteger(whole)
	if err != nil || n > math.MaxInt64/100 {
		return 0, strconv.ErrRange
	}
	var fraction int64
	if has {
		if len(part) < 1 || len(part) > 2 {
			return 0, strconv.ErrSyntax
		}
		if len(part) == 1 {
			part += "0"
		}
		fraction, err = parseInteger(part)
		if err != nil {
			return 0, err
		}
	}
	if n > (math.MaxInt64-fraction)/100 {
		return 0, strconv.ErrRange
	}
	return n*100 + fraction, nil
}
