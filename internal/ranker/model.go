package ranker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Example struct {
	Features Features
	Label    int
}
type TrainOptions struct {
	Iterations       int
	LearningRate, L2 float64
	TrainedAt        string
}
type Model struct {
	Version       string    `json:"version"`
	FeatureSchema string    `json:"feature_schema"`
	TrainSetSize  int       `json:"train_set_size"`
	TrainedAt     string    `json:"trained_at"`
	Weights       []float64 `json:"weights"`
	Bias          float64   `json:"bias"`
}

func sigmoid(v float64) float64 {
	if v >= 0 {
		return 1 / (1 + math.Exp(-v))
	}
	e := math.Exp(v)
	return e / (1 + e)
}
func (m Model) Predict(f Features) float64 {
	v := m.Bias
	for i, x := range f.Values() {
		if i < len(m.Weights) {
			v += m.Weights[i] * x
		}
	}
	return sigmoid(v)
}

func Train(examples []Example, options TrainOptions) (Model, error) {
	if len(examples) == 0 {
		return Model{}, fmt.Errorf("ranker: no examples")
	}
	for _, e := range examples {
		if e.Label != 0 && e.Label != 1 {
			return Model{}, fmt.Errorf("ranker: invalid label %d", e.Label)
		}
	}
	if options.Iterations <= 0 {
		options.Iterations = 500
	}
	if options.LearningRate <= 0 {
		options.LearningRate = .1
	}
	if options.L2 < 0 {
		options.L2 = 0
	}
	if options.TrainedAt == "" {
		options.TrainedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	m := Model{FeatureSchema: FeatureSchema(), TrainSetSize: len(examples), TrainedAt: options.TrainedAt, Weights: make([]float64, FeatureCount)}
	for step := 0; step < options.Iterations; step++ {
		grad := make([]float64, FeatureCount)
		bias := 0.0
		for _, e := range examples {
			p := m.Predict(e.Features)
			err := p - float64(e.Label)
			bias += err
			for i, x := range e.Features.Values() {
				grad[i] += err * x
			}
		}
		n := float64(len(examples))
		for i := range m.Weights {
			m.Weights[i] -= options.LearningRate * (grad[i]/n + options.L2*m.Weights[i])
		}
		m.Bias -= options.LearningRate * bias / n
	}
	m.Version = versionFor(m.FeatureSchema, m.TrainSetSize, m.TrainedAt)
	return m, nil
}
func versionFor(schema string, size int, at string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", schema, size, at)))
	return hex.EncodeToString(sum[:])[:16]
}
func modelPath(dir, version string) string { return filepath.Join(dir, "model-"+version+".json") }
func CurrentPath(dir string) string        { return filepath.Join(dir, "current") }
func SaveCandidate(dir string, model Model) error {
	if model.FeatureSchema != FeatureSchema() {
		return fmt.Errorf("ranker: cannot save model with stale feature schema")
	}
	if model.Version == "" {
		return fmt.Errorf("ranker: model version missing")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(model)
	if err != nil {
		return err
	}
	return os.WriteFile(modelPath(dir, model.Version), data, 0600)
}
func WriteCurrent(dir, value string) error {
	return os.WriteFile(CurrentPath(dir), []byte(value), 0600)
}

// Promote atomically updates the current pointer after eval has accepted a candidate.
func Promote(dir string, model Model) error {
	if err := SaveCandidate(dir, model); err != nil {
		return err
	}
	tmp := CurrentPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, []byte(model.Version+"\n"), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, CurrentPath(dir))
}
func LoadCurrent(dir string) (Model, error) {
	raw, err := os.ReadFile(CurrentPath(dir))
	if err != nil {
		return Model{}, err
	}
	version := strings.TrimSpace(string(raw))
	if version == "" {
		return Model{}, fmt.Errorf("ranker: empty current pointer")
	}
	data, err := os.ReadFile(modelPath(dir, version))
	if err != nil {
		return Model{}, err
	}
	var m Model
	if err := json.Unmarshal(data, &m); err != nil {
		return Model{}, err
	}
	if m.FeatureSchema != FeatureSchema() {
		return Model{}, fmt.Errorf("ranker: feature schema mismatch")
	}
	if m.Version != version || len(m.Weights) != FeatureCount {
		return Model{}, fmt.Errorf("ranker: invalid model")
	}
	return m, nil
}
