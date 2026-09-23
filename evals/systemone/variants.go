package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/systemone"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"go.yaml.in/yaml/v3"
)

// questionSet is the pair the gate asks. The default is the shipping wording;
// -questions swaps in a candidate from a file so a rewrite can be measured
// before it is committed — the thresholds are calibrated to exact wording,
// and this is how you find out what a change did to them.
type questionSet struct {
	Irreversible common.Question
	Secrets      common.Question
	Advisor      common.Question
	Name         string
}

func shippingQuestions() questionSet {
	return questionSet{
		Irreversible: systemone.IrreversibleQuestion(),
		Secrets:      systemone.SecretsQuestion(),
		Advisor:      systemone.AdvisorQuestion(),
		Name:         "shipping",
	}
}

// variantFile is the on-disk shape of a candidate: each question's
// instructions and criteria, the criteria in whatever shape the protocol
// allows (a string, or {true: …, false: …} with strings or objects inside).
type variantFile struct {
	Name         string          `yaml:"name"`
	Irreversible variantQuestion `yaml:"irreversible"`
	Secrets      variantQuestion `yaml:"secrets"`
	Advisor      variantQuestion `yaml:"advisor"`
}

type variantQuestion struct {
	Instructions string `yaml:"instructions"`
	Criteria     any    `yaml:"criteria"`
}

// loadVariant reads a candidate. A question left out keeps the shipping
// wording, so a file can change one question and hold the other fixed.
func loadVariant(path string) (questionSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return questionSet{}, err
	}
	var v variantFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&v); err != nil {
		return questionSet{}, fmt.Errorf("%s: %w", path, err)
	}
	qs := shippingQuestions()
	qs.Name = v.Name
	if qs.Name == "" {
		qs.Name = path
	}
	if v.Irreversible.Instructions != "" {
		qs.Irreversible = common.Question{Type: common.QuestionNoul, Instructions: v.Irreversible.Instructions, Criteria: v.Irreversible.Criteria}
	}
	if v.Secrets.Instructions != "" {
		qs.Secrets = common.Question{Type: common.QuestionNoul, Instructions: v.Secrets.Instructions, Criteria: v.Secrets.Criteria}
	}
	if v.Advisor.Instructions != "" {
		qs.Advisor = common.Question{Type: common.QuestionNoul, Instructions: v.Advisor.Instructions, Criteria: v.Advisor.Criteria}
	}
	return qs, nil
}
