package i18n

import "testing"

// TestT_KnownKey_ReturnsRequestedLang checks a straightforward hit: a seeded
// key returns the copy for the requested language.
func TestT_KnownKey_ReturnsRequestedLang(t *testing.T) {
	if got := T(LangES, "nav.overview"); got != "Resumen" {
		t.Errorf("T(LangES, %q) = %q, want %q", "nav.overview", got, "Resumen")
	}
	if got := T(LangEN, "nav.overview"); got != "Overview" {
		t.Errorf("T(LangEN, %q) = %q, want %q", "nav.overview", got, "Overview")
	}
}

// TestT_FallsBackToEnglishWhenRequestedLangMissing exercises the fallback
// chain's middle link: a lang the catalog entry doesn't carry falls back to
// the English copy (every seeded key carries es+en, so an unseeded lang like
// "fr" is the only way to reach this branch without faking catalog data).
func TestT_FallsBackToEnglishWhenRequestedLangMissing(t *testing.T) {
	got := T(Lang("fr"), "nav.overview")
	want := "Overview"
	if got != want {
		t.Errorf("T(fr, %q) = %q, want fallback to English %q", "nav.overview", got, want)
	}
}

// TestT_FallsBackToKeyWhenMissingEntirely is the final fallback link: a key
// absent from the catalog entirely renders as itself, so a missing
// translation is visible in the UI instead of blank.
func TestT_FallsBackToKeyWhenMissingEntirely(t *testing.T) {
	missing := "does.not.exist.anywhere"
	if got := T(LangES, missing); got != missing {
		t.Errorf("T(LangES, %q) = %q, want the key itself as fallback", missing, got)
	}
	if got := T(Lang("fr"), missing); got != missing {
		t.Errorf("T(fr, %q) = %q, want the key itself as fallback", missing, got)
	}
}

// TestAllCatalogEntriesHaveBothLanguages guards the catalog contract: every
// seeded key MUST carry both es and en copy, otherwise T()'s fallback chain
// could silently paper over a missing translation.
func TestAllCatalogEntriesHaveBothLanguages(t *testing.T) {
	for key, entry := range messages {
		if _, ok := entry[LangES]; !ok {
			t.Errorf("catalog key %q missing Spanish (es) copy", key)
		}
		if _, ok := entry[LangEN]; !ok {
			t.Errorf("catalog key %q missing English (en) copy", key)
		}
	}
}

// TestTf_Interpolation checks Sprintf-style interpolation on top of T.
func TestTf_Interpolation(t *testing.T) {
	messages["test.interp"] = map[Lang]string{
		LangES: "Hola %s",
		LangEN: "Hello %s",
	}
	defer delete(messages, "test.interp")

	if got := Tf(LangES, "test.interp", "Omnia"); got != "Hola Omnia" {
		t.Errorf("Tf(LangES, ...) = %q, want %q", got, "Hola Omnia")
	}
	if got := Tf(LangEN, "test.interp", "Omnia"); got != "Hello Omnia" {
		t.Errorf("Tf(LangEN, ...) = %q, want %q", got, "Hello Omnia")
	}
}
