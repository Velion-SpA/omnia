package i18n

import "testing"

// TestParseLang covers normalization: case-folding, trimming, region-code
// stripping (e.g. "es-CL" -> "es"), and the fallback to DefaultLang for
// anything unrecognized (including empty input).
func TestParseLang(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Lang
	}{
		{"lowercase es", "es", LangES},
		{"lowercase en", "en", LangEN},
		{"uppercase ES", "ES", LangES},
		{"uppercase EN", "EN", LangEN},
		{"mixed case", "Es", LangES},
		{"leading/trailing space", "  en  ", LangEN},
		{"region suffix hyphen", "es-CL", LangES},
		{"region suffix underscore", "en_US", LangEN},
		{"region suffix uppercase", "ES-CL", LangES},
		{"empty string defaults", "", DefaultLang},
		{"unknown language defaults", "fr", DefaultLang},
		{"garbage defaults", "not-a-lang", DefaultLang},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseLang(tt.in); got != tt.want {
				t.Errorf("ParseLang(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestDefaultLangIsSpanish pins the product requirement: the dashboard
// defaults to Spanish when no language preference is known.
func TestDefaultLangIsSpanish(t *testing.T) {
	if DefaultLang != LangES {
		t.Fatalf("DefaultLang = %q, want %q (Spanish default)", DefaultLang, LangES)
	}
}
