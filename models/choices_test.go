package models

import "testing"

type testReport struct {
	Model
	Status string
	Month  int
}

func (r *testReport) Schema(s *Schema) {
	s.Char(&r.Status, 20).LabeledChoices(Option("Open", "פתוח"), Option("Closed", "סגור")).Default("Open")
	s.Int(&r.Month).Choices(1, 2, 3)
}

func init() { Register[testReport]() }

func TestLabeledChoices(t *testing.T) {
	info := MustInfo[testReport]()
	status, month := info.FieldByName("status"), info.FieldByName("month")
	if len(status.Choices) != 2 || status.ChoiceLabel("Closed") != "סגור" {
		t.Fatalf("labeled choices: %v %q", status.Choices, status.ChoiceLabel("Closed"))
	}
	if month.ChoiceLabel(2) != "2" || status.ChoiceLabel("Other") != "Other" {
		t.Fatal("unlabeled choices should fall back to the humanized value")
	}
}
