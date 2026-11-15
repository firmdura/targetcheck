// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"testing"

	"github.com/firmdura/targetcheck/internal/model"
)

func coverageReport() model.Report {
	r := model.Report{}
	for _, name := range []model.VariantName{model.Baseline, model.Candidate} {
		r.Declared = append(r.Declared, model.VariantCoverage{Name: name, BuildIDs: []string{"build-0"}, TestIDs: []string{"first", "last"}})
		r.Variants = append(r.Variants, model.VariantReport{
			Name:  name,
			Build: []model.Check{{ID: "build-0", Index: 0, Kind: model.CheckBuild}},
			Tests: []model.Check{{ID: "first", Index: 0, Kind: model.CheckTest}, {ID: "last", Index: 1, Kind: model.CheckTest}},
		})
	}
	return r
}

func TestCoverageRejectsDeclaredButAbsentEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.Report)
	}{
		{"symmetric trailing tests omitted", func(r *model.Report) {
			for i := range r.Variants {
				r.Variants[i].Tests = r.Variants[i].Tests[:1]
			}
		}},
		{"trailing build omitted", func(r *model.Report) { r.Variants[0].Build = nil }},
		{"variant omitted", func(r *model.Report) { r.Variants = r.Variants[:1] }},
		{"duplicate variant", func(r *model.Report) { r.Variants[1].Name = model.Baseline }},
		{"declaration omitted", func(r *model.Report) { r.Declared = nil }},
		{"empty tests declared", func(r *model.Report) {
			for i := range r.Declared {
				r.Declared[i].TestIDs = nil
				r.Variants[i].Tests = nil
			}
		}},
		{"duplicate declared test", func(r *model.Report) { r.Declared[0].TestIDs[1] = "first" }},
		{"inconsistent shared test order", func(r *model.Report) { r.Declared[1].TestIDs[0], r.Declared[1].TestIDs[1] = "last", "first" }},
		{"duplicate check index", func(r *model.Report) { r.Variants[0].Tests[1].Index = 0 }},
		{"wrong declared ID", func(r *model.Report) { r.Variants[0].Tests[1].ID = "different" }},
		{"wrong check kind", func(r *model.Report) { r.Variants[0].Build[0].Kind = model.CheckTest }},
		{"wrong declared build order", func(r *model.Report) { r.Declared[0].BuildIDs[0] = "build-1" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := coverageReport()
			tc.mutate(&r)
			if err := model.ValidateCoverage(r); err == nil {
				t.Fatal("missing or inconsistent declared evidence accepted")
			}
		})
	}
	if err := model.ValidateCoverage(model.Report{}); err == nil {
		t.Fatal("zero report accepted")
	}
}

func TestCoverageAcceptsReorderedObservationsWithoutMutation(t *testing.T) {
	r := coverageReport()
	r.Variants[0], r.Variants[1] = r.Variants[1], r.Variants[0]
	for i := range r.Variants {
		r.Variants[i].Tests[0], r.Variants[i].Tests[1] = r.Variants[i].Tests[1], r.Variants[i].Tests[0]
	}
	before, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := model.ValidateCoverage(r); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("pure coverage validation mutated the report")
	}
}
