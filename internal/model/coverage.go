// SPDX-License-Identifier: Apache-2.0

package model

import "fmt"

// ValidateCoverage checks the report's observations against its independently
// retained declaration. It does not evaluate expectations, modify the report or
// return a behavioral verdict. The evaluator must reject coverage errors before
// considering matched; invalid-input reports may intentionally fail this check.
func ValidateCoverage(r Report) error {
	if len(r.Declared) != 2 || len(r.Variants) != 2 {
		return fmt.Errorf("both declared and observed variants are required")
	}
	declared := make(map[VariantName]VariantCoverage, 2)
	for _, d := range r.Declared {
		if d.Name != Baseline && d.Name != Candidate {
			return fmt.Errorf("unknown declared variant %q", d.Name)
		}
		if _, exists := declared[d.Name]; exists {
			return fmt.Errorf("duplicate declared variant %q", d.Name)
		}
		if len(d.TestIDs) == 0 || len(d.TestIDs) > MaxTests || len(d.BuildIDs) > MaxBuildSteps {
			return fmt.Errorf("invalid declared check count for %s", d.Name)
		}
		for i, id := range d.BuildIDs {
			if id != fmt.Sprintf("build-%d", i) {
				return fmt.Errorf("invalid declared build order for %s", d.Name)
			}
		}
		seen := make(map[string]bool, len(d.TestIDs))
		for _, id := range d.TestIDs {
			if id == "" || seen[id] {
				return fmt.Errorf("empty or duplicate declared test ID for %s", d.Name)
			}
			seen[id] = true
		}
		declared[d.Name] = d
	}
	b, c := declared[Baseline], declared[Candidate]
	if len(b.TestIDs) != len(c.TestIDs) {
		return fmt.Errorf("variants must share test declarations")
	}
	for i, id := range b.TestIDs {
		if c.TestIDs[i] != id {
			return fmt.Errorf("variants must share test order")
		}
	}
	seen := make(map[VariantName]bool, 2)
	for _, v := range r.Variants {
		d, exists := declared[v.Name]
		if !exists || seen[v.Name] {
			return fmt.Errorf("missing, duplicate or undeclared variant %q", v.Name)
		}
		seen[v.Name] = true
		if err := validateChecks(d.BuildIDs, v.Build, CheckBuild); err != nil {
			return fmt.Errorf("%s builds: %w", v.Name, err)
		}
		if err := validateChecks(d.TestIDs, v.Tests, CheckTest); err != nil {
			return fmt.Errorf("%s tests: %w", v.Name, err)
		}
	}
	return nil
}

func validateChecks(ids []string, checks []Check, kind CheckKind) error {
	if len(ids) != len(checks) {
		return fmt.Errorf("declared %d checks but received %d", len(ids), len(checks))
	}
	seen := make(map[int]bool, len(checks))
	for _, check := range checks {
		if check.Index < 0 || check.Index >= len(ids) || seen[check.Index] {
			return fmt.Errorf("missing, duplicate or invalid check index %d", check.Index)
		}
		if check.ID != ids[check.Index] || check.Kind != kind {
			return fmt.Errorf("check %q disagrees with its declaration", check.ID)
		}
		seen[check.Index] = true
	}
	return nil
}
