package intent

import (
	"fmt"
	"testing"
)

// labeledQuery is one hand-labelled fixture: a query and the intent a
// human reading it would assign. want == Unknown marks a query that must
// NOT be confidently routed — genuinely ambiguous, off-topic, or a
// deliberately close paraphrase that should not fool the classifier.
type labeledQuery struct {
	query string
	want  Intent
}

// fixtures is the hand-labelled corpus (plan P2: "A labelled fixture set +
// precision/recall test. The ship gate is classifier precision >= 0.9.").
// Roughly 15 examples per real intent, split ES/EN, plus a block of
// queries labelled Unknown — some flatly off-topic, some deliberately
// close paraphrases of a real cue that fall just short of it (testing that
// Unknown really is the fallback, not just the "nobody tried" case).
//
// Every ES/EN pair below is an independent, hand-written example of how a
// person would actually phrase that question kind, not a mechanical
// find/replace of the same sentence — mirroring
// internal/recall/bilingual_test.go's bilingualPairs, which the plan
// pointed to as this codebase's precedent for bilingual test fixtures.
var fixtures = []labeledQuery{
	// --- identity ----------------------------------------------------
	{"¿qué es Omnia?", Identity},
	{"qué es el sistema de fusión RRF", Identity},
	{"¿quién es Vel?", Identity},
	{"háblame sobre el proyecto Workly", Identity},
	{"cuéntame sobre el pipeline de recall", Identity},
	{"dame la definición de staleness downrank", Identity},
	{"qué son los claims en este sistema", Identity},
	{"what is Omnia", Identity},
	{"what's the recall package", Identity},
	{"who is Vel", Identity},
	{"tell me about the type lens boost", Identity},
	{"describe the ranking config", Identity},
	{"define adaptive floor", Identity},
	{"what is a claim in this system", Identity},
	{"what's a signature lane", Identity},

	// --- status --------------------------------------------------------
	{"¿cómo va Workly?", Status},
	{"cómo está el proyecto de ingest de docs", Status},
	{"cómo anda la migración de embeddings", Status},
	{"en qué estado está el pipeline de sync", Status},
	{"estado de la tarea de staleness", Status},
	{"cómo viene el sprint de recall", Status},
	{"qué estado tiene el endpoint de answer", Status},
	{"how is Workly going", Status},
	{"how's the ingest project doing", Status},
	{"what's the status of the embedding migration", Status},
	{"status of the sync pipeline", Status},
	{"how's the claim lifecycle work going", Status},
	{"what is the status of P4", Status},
	{"how is the doc ingestion feature going", Status},

	// --- delta -----------------------------------------------------------
	{"¿cómo arreglamos el bug de staleness?", Delta},
	{"cómo solucionamos el problema de embeddings duplicados", Delta},
	{"cómo resolvimos el error de sync", Delta},
	{"cómo se arregló el fallo de ranking", Delta},
	{"qué hicimos para arreglar el timeout de Ollama", Delta},
	{"how did we fix the duplicate embeddings bug", Delta},
	{"how did we solve the sync timeout", Delta},
	{"how did we resolve the ranking regression", Delta},
	{"how was this fixed", Delta},
	{"how was the staleness bug resolved", Delta},
	{"NullPointerException: cannot read property of undefined", Delta},
	{"panic: runtime error: invalid memory address", Delta},
	{"TypeError: cannot read properties of null", Delta},
	{"store.go:229: SearchDiag returned empty", Delta},
	{"fatal error: all goroutines are asleep", Delta},

	// --- open_items ------------------------------------------------------
	{"¿qué falta para terminar P2?", OpenItems},
	{"qué queda pendiente en el plan de retrieval", OpenItems},
	{"próximos pasos para el claim lifecycle", OpenItems},
	{"qué sigue después de la clasificación de intención", OpenItems},
	{"qué queda para el endpoint de answer", OpenItems},
	{"what's left before we ship P2", OpenItems},
	{"what is left on the claim lifecycle", OpenItems},
	{"what remains in the retrieval plan", OpenItems},
	{"what's next after intent classification", OpenItems},
	{"next steps for the answer endpoint", OpenItems},
	{"outstanding items on the plan", OpenItems},
	{"open items for the doc ingestion feature", OpenItems},
	{"what's next for the cross-project mode", OpenItems},
	{"what is next on the roadmap", OpenItems},

	// --- rationale ---------------------------------------------------
	{"¿por qué elegimos RRF en vez de un solo ranking?", Rationale},
	{"por qué decidimos separar salience de importance", Rationale},
	{"por qué usamos jina como modelo de embeddings", Rationale},
	{"cuál es la razón detrás del stable partition", Rationale},
	{"por qué se eligió SQLite para el store", Rationale},
	{"why did we choose reciprocal rank fusion", Rationale},
	{"why did we decide to keep recall dependency-free", Rationale},
	{"why do we use a half-life decay for recency", Rationale},
	{"why did we pick jina over bge-m3", Rationale},
	{"what was the reasoning behind the adaptive floor", Rationale},
	{"rationale for the type lens boost", Rationale},
	{"why did we go with a stable-partition lift instead of a hard filter", Rationale},
	{"why did we choose SQLite for the embeddings store", Rationale},
	{"why do we use word-boundary regex for lens signals", Rationale},

	// --- cross_project -------------------------------------------------
	{"¿en qué estoy bloqueado?", CrossProject},
	{"qué tengo bloqueado en todos mis proyectos", CrossProject},
	{"resumen semanal de todos los proyectos", CrossProject},
	{"en todos mis proyectos, qué me está bloqueando", CrossProject},
	{"qué tengo bloqueado en todos los proyectos", CrossProject},
	{"what am I blocked on across everything", CrossProject},
	{"across all projects, what's blocking me", CrossProject},
	{"all my projects this week", CrossProject},
	{"everything I'm working on this week", CrossProject},
	{"what am I blocked on", CrossProject},
	{"across everything, what needs attention", CrossProject},
	{"give me a summary across all projects", CrossProject},
	{"what's blocking me across all projects", CrossProject},
	{"across everything this week", CrossProject},

	// --- unknown: off-topic / neutral ------------------------------------
	{"", Unknown},
	{"hola", Unknown},
	{"gracias por tu ayuda", Unknown},
	{"list all observations from last month", Unknown},
	{"muéstrame las últimas notas", Unknown},
	{"the quick brown fox jumps over the lazy dog", Unknown},
	{"exportá el proyecto a CSV", Unknown},
	{"set the default project to omnia", Unknown},

	// --- unknown: close paraphrases that fall just short of a real cue ---
	{"cuál es el plan para esta tarde", Unknown},
	{"contame algo interesante", Unknown},
	{"list the projects", Unknown},
	{"can you summarize this document", Unknown},
	{"agregá una nota sobre el sprint", Unknown},
	{"mostrame el historial completo", Unknown},
}

// TestClassify_FixturePrecisionSmokeTest_NotTheShipGate is a self-authored
// smoke test ONLY — it is NOT the ship gate. These fixtures were written
// with the regex cue table in view, so a passing result here proves
// self-consistency, not classifier quality: this exact test reported
// precision 1.0000 while the real, blind-corpus precision (measured in
// blind_eval_test.go, TestClassify_BlindCorpusPrecisionGate_MustBeAtLeast0_90,
// against internal/eval/testdata/conversational_cases.json — a corpus this
// package's author never saw while tuning signals) was 0.4615 overall,
// with identity precision at 0.400. The permanent ship gate is the blind
// one; this test stays only as a fast regression check that a table edit
// didn't break an obviously-clear example.
//
// Precision here is defined over COMMITTED (non-Unknown) predictions only:
// of every fixture Classify routed to some real intent, what fraction did
// it route correctly? This is deliberately the metric the plan's own
// wording implies ("a misrouted query is worse than an unrouted one") —
// abstaining (predicting Unknown on a fixture whose true label IS one of
// the six real intents) costs recall, tracked and reported separately, but
// never counts against precision, because an abstention is not a
// misroute.
//
// A fixture whose true label is Unknown that gets committed to a real
// intent DOES count as a precision failure (a false positive) — the
// classifier confidently routed a query that should have been left alone,
// which is exactly the failure mode the gate exists to catch.
func TestClassify_FixturePrecisionSmokeTest_NotTheShipGate(t *testing.T) {
	const minPrecision = 0.9

	var committed, correct int
	var wrong []string
	for _, f := range fixtures {
		got := Classify(f.query)
		if got.Intent == Unknown {
			continue
		}
		committed++
		if got.Intent == f.want {
			correct++
		} else {
			wrong = append(wrong, fmt.Sprintf("Classify(%q) = %q (confidence %.2f, cue %q); want %q", f.query, got.Intent, got.Confidence, got.Cue, f.want))
		}
	}

	if committed == 0 {
		t.Fatal("classifier committed to zero non-Unknown intents across the whole fixture corpus; precision is undefined")
	}
	precision := float64(correct) / float64(committed)

	t.Logf("intent classifier precision: %d/%d = %.4f (gate: >= %.2f)", correct, committed, precision, minPrecision)
	if precision < minPrecision {
		t.Errorf("SHIP GATE FAILED: intent classifier precision %.4f (%d/%d committed predictions correct) is below the required %.2f (plan P2: 'a misrouted query is worse than an unrouted one'). Misrouted fixtures:\n%s",
			precision, correct, committed, minPrecision, joinLines(wrong))
	}
}

// TestClassify_RecallByIntent reports (not gates on) per-intent recall
// against fixtures — the plan's own measurement note: "each of the seven
// classes scored separately — today they are all one number, which is why
// this problem was invisible." Recall is not part of the ship gate
// (precision is), but a silent recall collapse on one intent while overall
// precision stays high would be exactly the kind of regression this test
// exists to surface via t.Log, even though it does not fail the build.
func TestClassify_RecallByIntent(t *testing.T) {
	total := map[Intent]int{}
	found := map[Intent]int{}
	for _, f := range fixtures {
		if f.want == Unknown {
			continue
		}
		total[f.want]++
		if Classify(f.query).Intent == f.want {
			found[f.want]++
		}
	}
	for _, i := range []Intent{Identity, Status, Delta, OpenItems, Rationale, CrossProject} {
		t.Logf("recall[%s] = %d/%d", i, found[i], total[i])
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += "  " + l + "\n"
	}
	return out
}
