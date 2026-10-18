package admin

import (
	"testing"
	"time"

	"github.com/Bokitza/djan-go/models"
	"github.com/shopspring/decimal"
)

func TestDecimalAndDateInput(t *testing.T) {
	amount := &models.Field{Kind: models.KindDecimal, MaxDigits: 6, Places: 2}
	value, err := parseInput(amount, "1234.5", true)
	if err != nil || !value.(decimal.Decimal).Equal(decimal.RequireFromString("1234.50")) {
		t.Fatalf("valid decimal: %v %v", value, err)
	}
	for raw, want := range map[string]string{
		"1.234":   "Ensure that there are no more than 2 decimal places.",
		"12345.1": "Ensure that there are no more than 4 digits before the decimal point.",
		"abc":     "Enter a number.",
	} {
		if _, err := parseInput(amount, raw, true); err == nil || err.Error() != want {
			t.Errorf("%q: got %v, want %q", raw, err, want)
		}
	}

	day := &models.Field{Kind: models.KindDate}
	value, err = parseInput(day, "2026-10-11", true)
	if err != nil || value.(time.Time).Format(time.DateOnly) != "2026-10-11" {
		t.Fatalf("valid date: %v %v", value, err)
	}
	if _, err := parseInput(day, "11/10/2026", true); err == nil {
		t.Fatal("dates must use the ISO format of date inputs")
	}

	for value, want := range map[string]string{
		"1234567.5": "1,234,567.5",
		"-1000":     "-1,000",
		"0.125":     "0.13",
		"12.00":     "12",
	} {
		if got := formatNumber(decimal.RequireFromString(value), 2); got != want {
			t.Errorf("formatNumber(%s) = %q, want %q", value, got, want)
		}
	}
}
