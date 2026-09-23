package style

import "testing"

func TestHexAndContrast(t *testing.T) {
	for in, want := range map[string]string{"#b33": "#bb3333", "#AbCdEf": "#abcdef", "red": "", "#12": ""} {
		if got, _ := Hex(in); got != want {
			t.Errorf("Hex(%q) = %q, want %q", in, got, want)
		}
	}
	if Contrast("#f9d900") != "black" || Contrast("#e50000") != "white" || Contrast("#ffffff") != "black" {
		t.Error("contrast picks the wrong text colour")
	}
}

func TestChipIsReadable(t *testing.T) {
	if got := Chip("Minor", "#f9d900"); got != "\x1b[30;48;2;249;217;0m Minor \x1b[0m" {
		t.Errorf("yellow chip = %q", got)
	}
	if got := Chip("Critical", "#e50000"); got != "\x1b[97;48;2;229;0;0m Critical \x1b[0m" {
		t.Errorf("red chip = %q", got)
	}
	if got := Strip(Chip("x", "")); got != " x " {
		t.Errorf("fallback chip = %q", got)
	}
}

func TestCellFit(t *testing.T) {
	if got := (Cell{Text: "Hello world"}).Fit(8); got != "Hello w…" {
		t.Errorf("truncate = %q", got)
	}
	if got := Width(Cell{Text: "hi", Style: Bold}.Fit(5)); got != 5 {
		t.Errorf("padded width = %d", got)
	}
}
