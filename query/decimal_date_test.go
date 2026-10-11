package query

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yoav-rogat/djan-go/testdb"
	"github.com/yoav-rogat/djan-go/models"
)

type ledgerEntry struct {
	models.Model
	Amount  decimal.Decimal
	Balance *decimal.Decimal
	Day     time.Time
	DueDay  *time.Time
}

func (l *ledgerEntry) Schema(s *models.Schema) {
	s.App("press")
	s.Decimal(&l.Amount, 12, 2)
	s.NullableDecimal(&l.Balance, 50, 5)
	s.Date(&l.Day)
	s.NullableDate(&l.DueDay)
}

func init() { models.Register[ledgerEntry]() }

func TestDecimalAndDateFields(t *testing.T) {
	info := models.MustInfo[ledgerEntry]()
	if got := info.FieldByName("amount").DatabaseType(); got != "numeric(12,2)" {
		t.Fatalf("amount column type %q", got)
	}
	if got := info.FieldByName("day").DatabaseType(); got != "date" {
		t.Fatalf("day column type %q", got)
	}
	if !info.FieldByName("balance").Nullable || !info.FieldByName("due_day").Nullable {
		t.Fatal("nullable decimal and date fields should be nullable")
	}

	ctx, _ := testdb.New(t)
	testdb.CreateTables(t, ctx)

	day := func(value string) time.Time {
		parsed, err := time.Parse(time.DateOnly, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	balance := decimal.RequireFromString("1234567.12345")
	entries := []*ledgerEntry{
		{Amount: decimal.RequireFromString("10.50"), Day: day("2026-01-15"), Balance: &balance},
		{Amount: decimal.RequireFromString("99.99"), Day: day("2026-03-01")},
		{Amount: decimal.RequireFromString("-3.10"), Day: day("2025-12-31")},
	}
	for _, entry := range entries {
		if err := Create(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := Objects[ledgerEntry]().GetByPK(ctx, entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Amount.Equal(decimal.RequireFromString("10.5")) || loaded.Balance == nil || !loaded.Balance.Equal(balance) {
		t.Fatalf("decimal round trip: %v %v", loaded.Amount, loaded.Balance)
	}
	if loaded.Day.Format(time.DateOnly) != "2026-01-15" || loaded.DueDay != nil {
		t.Fatalf("date round trip: %v %v", loaded.Day, loaded.DueDay)
	}

	large, err := Objects[ledgerEntry]().Where(func(l *ledgerEntry) Cond {
		return Gt(&l.Amount, decimal.NewFromInt(10))
	}).Count(ctx)
	if err != nil || large != 2 {
		t.Fatalf("decimal comparison: %d %v", large, err)
	}
	thisYear, err := Objects[ledgerEntry]().Where(func(l *ledgerEntry) Cond {
		return Gte(&l.Day, day("2026-01-01"))
	}).Count(ctx)
	if err != nil || thisYear != 2 {
		t.Fatalf("date comparison: %d %v", thisYear, err)
	}

	due := day("2026-02-01")
	updated, err := Objects[ledgerEntry]().Where(func(l *ledgerEntry) Cond { return Lt(&l.Amount, decimal.Zero) }).
		Update(ctx, func(l *ledgerEntry) []Assign { return []Assign{Set(&l.DueDay, &due)} })
	if err != nil || updated != 1 {
		t.Fatalf("update nullable date: %d %v", updated, err)
	}
	values, err := Objects[ledgerEntry]().Where(func(l *ledgerEntry) Cond { return IsNull(&l.DueDay, false) }).Values(ctx, "amount", "due_day")
	if err != nil || len(values) != 1 {
		t.Fatalf("values: %v %v", values, err)
	}
	if amount, ok := values[0]["amount"].(decimal.Decimal); ok && !strings.HasPrefix(amount.String(), "-3.1") {
		t.Fatalf("values amount: %v", values[0]["amount"])
	}
}
