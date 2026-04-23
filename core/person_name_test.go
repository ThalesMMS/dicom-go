package core

import "testing"

func TestParsePersonName(t *testing.T) {
	pn := ParsePersonName(" Doe ^ John ^ Quincy ^ Dr. ^ Jr. ")
	if pn.FamilyName != "Doe" || pn.GivenName != "John" || pn.MiddleName != "Quincy" || pn.NamePrefix != "Dr." || pn.NameSuffix != "Jr." {
		t.Fatalf("ParsePersonName() = %+v", pn)
	}
}

func TestPersonNameStringAndSerialization(t *testing.T) {
	pn := PersonName{
		FamilyName: "Doe",
		GivenName:  "John",
		NamePrefix: "Dr.",
	}
	if got := pn.String(); got != "Dr. John Doe" {
		t.Fatalf("String() = %q, want %q", got, "Dr. John Doe")
	}
	if got := pn.ToDICOMString(); got != "Doe^John^^Dr." {
		t.Fatalf("ToDICOMString() = %q, want %q", got, "Doe^John^^Dr.")
	}
}

func TestPersonNameRoundTripAndEmpty(t *testing.T) {
	if got := ParsePersonName("").ToDICOMString(); got != "" {
		t.Fatalf("empty round trip = %q, want empty", got)
	}

	want := "Adams^John^Robert^Rev.^B.A. M.Div."
	if got := ParsePersonName(want).ToDICOMString(); got != want {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

func TestPersonNameIsEmpty(t *testing.T) {
	if !(PersonName{}).IsEmpty() {
		t.Fatal("zero PersonName should be empty")
	}
	if ParsePersonName("Doe").IsEmpty() {
		t.Fatal("non-empty PersonName should not be empty")
	}
}

func TestParsePersonNameWithIdeographic(t *testing.T) {
	input := "Yamada^Tarou=山田^太郎"
	pn := ParsePersonName(input)
	if pn.FamilyName != "Yamada" {
		t.Fatalf("FamilyName = %q, want %q", pn.FamilyName, "Yamada")
	}
	if pn.GivenName != "Tarou" {
		t.Fatalf("GivenName = %q, want %q", pn.GivenName, "Tarou")
	}
	if got := pn.ToDICOMString(); got != input {
		t.Fatalf("ToDICOMString() = %q, want %q", got, input)
	}
}
