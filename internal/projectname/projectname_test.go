package projectname

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"already lower", "engram", "engram"},
		{"mixed case", "Engram", "engram"},
		{"upper case", "ENGRAM", "engram"},
		{"leading/trailing whitespace", "  engram  ", "engram"},
		{"single hyphen preserved", "Engram-Memory", "engram-memory"},
		{"double hyphen collapses", "engram--memory", "engram-memory"},
		{"double underscore collapses", "engram__memory", "engram_memory"},
		{"triple hyphen collapses", "engram---memory", "engram-memory"},
		{"quadruple underscore collapses", "engram____memory", "engram_memory"},
		{"mixed repeated separators", "my--project__name", "my-project_name"},
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},
		{"already normalized", "already-lower", "already-lower"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Normalize(tc.input)
			if got != tc.want {
				t.Errorf("Normalize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNormalizeOrUnknown(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty falls back to unknown", "", "unknown"},
		{"whitespace only falls back to unknown", "   ", "unknown"},
		{"normal name passes through", "MyProject", "myproject"},
		{"collapses separators like Normalize", "my--project", "my-project"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeOrUnknown(tc.input)
			if got != tc.want {
				t.Errorf("NormalizeOrUnknown(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
