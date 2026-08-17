package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// makeConvCase builds one minimal, individually-valid ConversationalCase for
// a non-absence kind, so per-field validation tests can isolate the rule
// under test from every other one. KindCrossProject gets Unscoped=true and
// no Project (its own required shape, engram #2623); every other kind gets
// a Project so it stays independently valid against the project/unscoped
// gate this same fix added.
func makeConvCase(id string, kind QuestionKind) ConversationalCase {
	c := ConversationalCase{
		ID:            id,
		Kind:          kind,
		Query:         "query",
		Language:      LanguageEN,
		ObservationID: "obs-" + id,
		ExpectedFact:  "fact",
	}
	if kind == KindCrossProject {
		c.Unscoped = true
	} else {
		c.Project = "test-project"
	}
	return c
}

// makeFullConvCorpus builds one valid case per AllQuestionKinds entry (the
// minimum MinCasesPerKind requires), absence cases shaped per its own
// contract.
func makeFullConvCorpus() []ConversationalCase {
	cases := make([]ConversationalCase, 0, len(AllQuestionKinds))
	for _, k := range AllQuestionKinds {
		if k == KindAbsence {
			cases = append(cases, ConversationalCase{
				ID:            "case-" + string(k),
				Kind:          k,
				Query:         "query",
				Language:      LanguageEN,
				ExpectAbsence: true,
				// Absence cases are scoped like every other non-cross_project
				// kind (see this file's design decision doc + the loader's
				// own gate): a real consumer still routes an absence
				// question to some project.
				Project: "test-project",
			})
			continue
		}
		cases = append(cases, makeConvCase("case-"+string(k), k))
	}
	return cases
}

func writeConvCasesFile(t *testing.T, cases []ConversationalCase) string {
	t.Helper()
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatalf("marshal cases: %v", err)
	}
	path := filepath.Join(t.TempDir(), "conversational_cases.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write temp corpus file: %v", err)
	}
	return path
}

func TestLoadConversationalCorpus_AcceptsOneCasePerKind(t *testing.T) {
	path := writeConvCasesFile(t, makeFullConvCorpus())
	got, err := LoadConversationalCorpus(path)
	if err != nil {
		t.Fatalf("LoadConversationalCorpus: unexpected error: %v", err)
	}
	if len(got) != len(AllQuestionKinds) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(AllQuestionKinds))
	}
}

func TestLoadConversationalCorpus_RequiresEveryKindRepresented(t *testing.T) {
	cases := makeFullConvCorpus()
	// Drop the identity case entirely — every other kind still present.
	trimmed := make([]ConversationalCase, 0, len(cases))
	for _, c := range cases {
		if c.Kind == KindIdentity {
			continue
		}
		trimmed = append(trimmed, c)
	}
	path := writeConvCasesFile(t, trimmed)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error when a whole kind (identity) has zero cases, got nil")
	}
}

func TestLoadConversationalCorpus_RequiresValidKind(t *testing.T) {
	cases := makeFullConvCorpus()
	cases[0].Kind = QuestionKind("not-a-real-kind")
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error for an unknown kind, got nil")
	}
}

func TestLoadConversationalCorpus_RequiresValidLanguage(t *testing.T) {
	cases := makeFullConvCorpus()
	cases[0].Language = Language("En") // typo'd
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error for an invalid language, got nil")
	}
}

func TestLoadConversationalCorpus_RequiresQuery(t *testing.T) {
	cases := makeFullConvCorpus()
	for i := range cases {
		if cases[i].Kind == KindStatus {
			cases[i].Query = ""
		}
	}
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error for a case with an empty query, got nil")
	}
}

func TestLoadConversationalCorpus_RejectsDuplicateID(t *testing.T) {
	cases := makeFullConvCorpus()
	cases[1].ID = cases[0].ID
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error for two cases sharing the same ID, got nil")
	}
}

// TestLoadConversationalCorpus_AbsenceMustSetExpectAbsence is requirement
// 3's contract: an absence-kind case with expect_absence left false is a
// corpus authoring bug — the gold answer for that kind IS "no evidence
// exists", so leaving the flag off silently drops the whole point of the
// kind.
func TestLoadConversationalCorpus_AbsenceMustSetExpectAbsence(t *testing.T) {
	cases := makeFullConvCorpus()
	for i := range cases {
		if cases[i].Kind == KindAbsence {
			cases[i].ExpectAbsence = false
		}
	}
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error for an absence case with expect_absence=false, got nil")
	}
}

// TestLoadConversationalCorpus_AbsenceForbidsGoldFields is the flip side:
// an absence case that ALSO carries an observation_id/expected_fact
// contradicts its own gold answer ("nothing should surface").
func TestLoadConversationalCorpus_AbsenceForbidsGoldFields(t *testing.T) {
	t.Run("observation_id set", func(t *testing.T) {
		cases := makeFullConvCorpus()
		for i := range cases {
			if cases[i].Kind == KindAbsence {
				cases[i].ObservationID = "obs-should-not-be-here"
			}
		}
		path := writeConvCasesFile(t, cases)
		if _, err := LoadConversationalCorpus(path); err == nil {
			t.Error("expected error for an absence case carrying observation_id, got nil")
		}
	})
	t.Run("expected_fact set", func(t *testing.T) {
		cases := makeFullConvCorpus()
		for i := range cases {
			if cases[i].Kind == KindAbsence {
				cases[i].ExpectedFact = "should not be here"
			}
		}
		path := writeConvCasesFile(t, cases)
		if _, err := LoadConversationalCorpus(path); err == nil {
			t.Error("expected error for an absence case carrying expected_fact, got nil")
		}
	})
}

// TestLoadConversationalCorpus_NonAbsenceForbidsExpectAbsence guards the
// other direction: expect_absence is reserved for KindAbsence.
func TestLoadConversationalCorpus_NonAbsenceForbidsExpectAbsence(t *testing.T) {
	cases := makeFullConvCorpus()
	for i := range cases {
		if cases[i].Kind == KindStatus {
			cases[i].ExpectAbsence = true
		}
	}
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error for a non-absence case with expect_absence=true, got nil")
	}
}

// TestLoadConversationalCorpus_CrossProjectRequiresUnscoped is engram
// #2623's core gate: a cross_project case's whole point is a search
// spanning every project, so it must declare unscoped=true explicitly — a
// cross_project case that forgot to set it must fail loudly at load time,
// not silently scope to a single (empty) project.
func TestLoadConversationalCorpus_CrossProjectRequiresUnscoped(t *testing.T) {
	cases := makeFullConvCorpus()
	for i := range cases {
		if cases[i].Kind == KindCrossProject {
			cases[i].Unscoped = false
		}
	}
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error for a cross_project case with unscoped=false, got nil")
	}
}

// TestLoadConversationalCorpus_CrossProjectForbidsProject is the flip side:
// a cross_project case that sets BOTH unscoped=true AND a project
// contradicts its own gold shape (search every project vs. search exactly
// one).
func TestLoadConversationalCorpus_CrossProjectForbidsProject(t *testing.T) {
	cases := makeFullConvCorpus()
	for i := range cases {
		if cases[i].Kind == KindCrossProject {
			cases[i].Project = "omnia"
		}
	}
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error for a cross_project case carrying both unscoped=true and project, got nil")
	}
}

// TestLoadConversationalCorpus_NonCrossProjectForbidsUnscoped guards the
// other direction: unscoped=true is reserved for KindCrossProject, so a
// scoped-kind case can never accidentally search every project.
func TestLoadConversationalCorpus_NonCrossProjectForbidsUnscoped(t *testing.T) {
	cases := makeFullConvCorpus()
	for i := range cases {
		if cases[i].Kind == KindStatus {
			cases[i].Unscoped = true
		}
	}
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err == nil {
		t.Error("expected error for a non-cross_project case with unscoped=true, got nil")
	}
}

// TestLoadConversationalCorpus_NonCrossProjectRequiresProject covers every
// non-cross_project kind (including absence, per this fix's judgement call
// that a real consumer still routes an absence question to some project):
// an empty Project must fail loudly rather than silently becoming an
// unscoped, cross-project search (engram #2623's whole point).
func TestLoadConversationalCorpus_NonCrossProjectRequiresProject(t *testing.T) {
	for _, k := range AllQuestionKinds {
		if k == KindCrossProject {
			continue
		}
		k := k
		t.Run(string(k), func(t *testing.T) {
			cases := makeFullConvCorpus()
			for i := range cases {
				if cases[i].Kind == k {
					cases[i].Project = ""
				}
			}
			path := writeConvCasesFile(t, cases)
			if _, err := LoadConversationalCorpus(path); err == nil {
				t.Errorf("expected error for a %s case missing project, got nil", k)
			}
		})
	}
}

// TestLoadConversationalCorpus_NonAbsenceRequiresExpectedFact covers every
// non-absence kind, including identity — identity cases may omit
// ObservationID (see the struct doc) but must still carry an ExpectedFact,
// or the grounding metric would have nothing to check.
func TestLoadConversationalCorpus_NonAbsenceRequiresExpectedFact(t *testing.T) {
	for _, k := range AllQuestionKinds {
		if k == KindAbsence {
			continue
		}
		k := k
		t.Run(string(k), func(t *testing.T) {
			cases := makeFullConvCorpus()
			for i := range cases {
				if cases[i].Kind == k {
					cases[i].ExpectedFact = ""
				}
			}
			path := writeConvCasesFile(t, cases)
			if _, err := LoadConversationalCorpus(path); err == nil {
				t.Errorf("expected error for a %s case missing expected_fact, got nil", k)
			}
		})
	}
}

// TestLoadConversationalCorpus_IdentityMayOmitObservationID is the
// documented exception (ConversationalCase.ObservationID's doc): identity
// cases legitimately have no gold observation until P1 (repodoc ingestion)
// ships, so an identity case with ExpectedFact but no ObservationID must
// load cleanly.
func TestLoadConversationalCorpus_IdentityMayOmitObservationID(t *testing.T) {
	cases := makeFullConvCorpus()
	for i := range cases {
		if cases[i].Kind == KindIdentity {
			cases[i].ObservationID = ""
		}
	}
	path := writeConvCasesFile(t, cases)
	if _, err := LoadConversationalCorpus(path); err != nil {
		t.Errorf("identity case with no observation_id must load fine, got: %v", err)
	}
}

// TestEmbeddedConversationalCorpus_LoadsAndValidates is the go:embed
// cwd-independence guarantee (mirroring TestEmbeddedCorpus in corpus_test.go
// for the coding-agent corpus, if present) applied to the hand-authored
// seed corpus: it must parse and validate regardless of the test binary's
// working directory.
func TestEmbeddedConversationalCorpus_LoadsAndValidates(t *testing.T) {
	cases, err := EmbeddedConversationalCorpus()
	if err != nil {
		t.Fatalf("EmbeddedConversationalCorpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("embedded conversational corpus is empty")
	}

	seenKinds := make(map[QuestionKind]int, len(AllQuestionKinds))
	seenLangs := make(map[Language]int, 2)
	for _, c := range cases {
		seenKinds[c.Kind]++
		seenLangs[c.Language]++
	}
	for _, k := range AllQuestionKinds {
		if seenKinds[k] == 0 {
			t.Errorf("embedded corpus has zero cases for kind %q", k)
		}
	}
	if seenLangs[LanguageEN] == 0 || seenLangs[LanguageES] == 0 {
		t.Errorf("embedded corpus must be bilingual (ES/EN): got %v", seenLangs)
	}
}

// TestEmbeddedConversationalCorpus_ExpectedFactsAreVerbatimNonEmpty guards
// against the exact #1898 mistake (see the struct doc's hard requirement):
// every non-absence case must carry a non-empty, non-whitespace
// ExpectedFact — an empty/blank "verbatim" excerpt would silently make that
// case unscoreable-as-a-hit forever, the same failure shape #1912 traced
// back to a normalization bug.
func TestEmbeddedConversationalCorpus_ExpectedFactsAreVerbatimNonEmpty(t *testing.T) {
	cases, err := EmbeddedConversationalCorpus()
	if err != nil {
		t.Fatalf("EmbeddedConversationalCorpus: %v", err)
	}
	for _, c := range cases {
		if c.Kind == KindAbsence {
			continue
		}
		if len(c.ExpectedFact) < 5 {
			t.Errorf("case %q: expected_fact %q looks too short to be a real verbatim excerpt", c.ID, c.ExpectedFact)
		}
	}
}
