// Package ranker implements the optional, local learned ranking model.
package ranker

import (
	"math"
	"strings"
	"time"

	"github.com/velion/omnia/internal/config"
)

const FeatureCount = 6
const featureSchema = "lexical_rrf:v1,semantic_cosine:v1,recency:v1,importance:v1,outcome_judgment:v1,type_match:v1"

// Candidate contains only signals already present in recall, config, or store.
type Candidate struct {
	LexicalRRF     float64
	SemanticCosine float64
	UpdatedAt      time.Time
	Type           string
	TypeMatch      bool
	Outcome        string
	Judgment       string
}

// Features is the normalized model input. All values are in [0,1].
type Features struct{ LexicalRRF, SemanticCosine, Recency, Importance, OutcomeHistory, TypeMatch float64 }

func (f Features) Values() []float64 {
	return []float64{f.LexicalRRF, f.SemanticCosine, f.Recency, f.Importance, f.OutcomeHistory, f.TypeMatch}
}
func (f Features) Named() map[string]float64 {
	return map[string]float64{"lexical_rrf": f.LexicalRRF, "semantic_cosine": f.SemanticCosine, "recency": f.Recency, "importance": f.Importance, "outcome_judgment": f.OutcomeHistory, "type_match": f.TypeMatch}
}
func FeatureSchema() string { return featureSchema }

func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// BuildFeatures normalizes only existing retrieval and observation signals.
func BuildFeatures(c Candidate, ranking config.RankingConfig, now time.Time) Features {
	halfLife := ranking.RecencyHalfLifeDays
	if halfLife <= 0 {
		halfLife = 14
	}
	recency := 0.0
	if !c.UpdatedAt.IsZero() {
		days := now.Sub(c.UpdatedAt).Hours() / 24
		if days < 0 {
			days = 0
		}
		recency = math.Pow(.5, days/halfLife)
	}
	importance := float64(config.DefaultImportanceWeight(c.Type)) / float64(config.DefaultImportanceWeight("decision"))
	history := 0.5
	switch strings.ToLower(strings.TrimSpace(c.Outcome)) {
	case "worked":
		history = 1
	case "did_not_work":
		history = 0
	}
	switch strings.ToLower(strings.TrimSpace(c.Judgment)) {
	case "compatible":
		history = 1
	case "supersedes", "conflicts_with":
		history = 0
	}
	match := 0.0
	if c.TypeMatch {
		match = 1
	}
	return Features{clamp(c.LexicalRRF), clamp(c.SemanticCosine), clamp(recency), clamp(importance), history, match}
}

// Label derives training labels from existing outcome and relation vocabularies.
func Label(c Candidate) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(c.Outcome)) {
	case "worked":
		return 1, true
	case "did_not_work":
		return 0, true
	}
	switch strings.ToLower(strings.TrimSpace(c.Judgment)) {
	case "compatible":
		return 1, true
	case "supersedes", "conflicts_with":
		return 0, true
	}
	return 0, false
}
