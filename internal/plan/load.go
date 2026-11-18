// SPDX-License-Identifier: Apache-2.0

// Package plan validates manifests and declared filesystem identities without
// executing programs, writing workspace files or evaluating test expectations.
package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/firmdura/targetcheck/internal/model"
)

// Service implements the frozen planner interface; its zero value has no state.
type Service struct{}

var _ model.Planner = Service{}

func (Service) Load(r io.Reader) (model.Spec, error)                      { return Load(r) }
func (Service) Prepare(s model.Spec, o model.Options) (model.Plan, error) { return Prepare(s, o) }
func (Service) InspectArtifacts(p model.Plan, v model.VariantPlan) ([]model.ArtifactIdentity, error) {
	return InspectArtifacts(p, v)
}

// Load reads exactly one strict v1 manifest, including a duplicate-key pass.
func Load(r io.Reader) (model.Spec, error) {
	if r == nil {
		return model.Spec{}, invalid("manifest reader is nil", nil)
	}
	data, err := io.ReadAll(io.LimitReader(r, model.MaxManifestBytes+1))
	if err != nil {
		return model.Spec{}, invalid("read manifest", err)
	}
	if len(data) > model.MaxManifestBytes {
		return model.Spec{}, invalid("manifest exceeds 1 MiB", nil)
	}
	if !utf8.Valid(data) {
		return model.Spec{}, invalid("manifest is not valid UTF-8", nil)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := readValue(dec, 0)
	if err == nil {
		if _, end := dec.Token(); end != io.EOF {
			err = end
			if err == nil {
				err = fmt.Errorf("manifest must contain exactly one JSON value")
			}
		}
	}
	if err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return model.Spec{}, invalid(fmt.Sprintf("invalid JSON manifest at byte %d", syntax.Offset), err)
		}
		return model.Spec{}, invalid("invalid JSON manifest", err)
	}
	if err = wireShape(value); err != nil {
		return model.Spec{}, invalid("invalid manifest shape", err)
	}
	var spec model.Spec
	if err = json.Unmarshal(data, &spec); err != nil {
		return model.Spec{}, invalid("invalid manifest field type", err)
	}
	if err = validateSpec(spec); err != nil {
		return model.Spec{}, err
	}
	spec.ManifestJSON = append([]byte(nil), data...)
	return spec, nil
}

func readValue(dec *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("JSON nesting exceeds v1 limit")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		v := map[string]any{}
		for dec.More() {
			tok, err = dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := tok.(string)
			if !ok {
				return nil, fmt.Errorf("object key is not string")
			}
			if _, exists := v[key]; exists {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			child, err := readValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			v[key] = child
		}
		_, err = dec.Token()
		return v, err
	case '[':
		v := []any{}
		for dec.More() {
			child, err := readValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			v = append(v, child)
		}
		_, err = dec.Token()
		return v, err
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

// wireShape handles exact spelling, required members and nulls before Go's
// case-insensitive struct decoder can erase those distinctions. Semantic values
// and filesystem checks are separately validated below.
func wireShape(value any) error {
	top, err := object(value, []string{"schema_version", "target", "variants", "test_inputs", "tests"}, nil)
	if err != nil {
		return err
	}
	target, err := object(top["target"], []string{"kind"}, []string{"executable", "sysroot"})
	if err != nil {
		return err
	}
	if target["kind"] == "native" && len(target) != 1 {
		return fmt.Errorf("native target has QEMU-only fields")
	}
	variants, err := object(top["variants"], []string{"baseline", "candidate"}, nil)
	if err != nil {
		return err
	}
	for _, name := range []string{"baseline", "candidate"} {
		v, err := object(variants[name], []string{"cwd", "build", "inputs", "artifacts"}, nil)
		if err != nil {
			return err
		}
		if err = stringArray(v["inputs"]); err != nil {
			return err
		}
		if err = stringArray(v["artifacts"]); err != nil {
			return err
		}
		build, ok := v["build"].([]any)
		if !ok {
			return fmt.Errorf("build must be an array")
		}
		for _, item := range build {
			command, err := object(item, []string{"argv", "timeout_seconds"}, []string{"env"})
			if err != nil {
				return err
			}
			if err = stringArray(command["argv"]); err != nil {
				return err
			}
			if raw, ok := command["env"]; ok {
				env, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("env must be an object")
				}
				for _, v := range env {
					if _, ok := v.(string); !ok {
						return fmt.Errorf("env values must be strings")
					}
				}
			}
		}
	}
	if err = stringArray(top["test_inputs"]); err != nil {
		return err
	}
	tests, ok := top["tests"].([]any)
	if !ok {
		return fmt.Errorf("tests must be an array")
	}
	for _, item := range tests {
		test, err := object(item, []string{"id", "argv", "timeout_seconds", "baseline_expect", "candidate_expect"}, nil)
		if err != nil {
			return err
		}
		if err = stringArray(test["argv"]); err != nil {
			return err
		}
		for _, name := range []string{"baseline_expect", "candidate_expect"} {
			e, err := object(test[name], []string{"exit_code"}, []string{"stdout_contains", "stderr_contains"})
			if err != nil {
				return err
			}
			for _, key := range []string{"stdout_contains", "stderr_contains"} {
				if val, exists := e[key]; exists {
					if text, ok := val.(string); !ok || text == "" {
						return fmt.Errorf("supplied expectation marker must be nonempty string")
					}
				}
			}
		}
	}
	return nil
}

func object(value any, required, optional []string) (map[string]any, error) {
	v, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected JSON object")
	}
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
		if val, exists := v[key]; !exists || val == nil {
			return nil, fmt.Errorf("missing or null field %q", key)
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key, val := range v {
		if !allowed[key] {
			return nil, fmt.Errorf("unknown field %q", key)
		}
		if val == nil {
			return nil, fmt.Errorf("null field %q", key)
		}
	}
	return v, nil
}

func stringArray(value any) error {
	v, ok := value.([]any)
	if !ok {
		return fmt.Errorf("expected string array")
	}
	for _, item := range v {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("expected string array item")
		}
	}
	return nil
}

func invalid(message string, cause error) error {
	if cause != nil {
		message += ": " + cause.Error()
	}
	return &model.RunError{Kind: model.ErrorInvalidInput, Failure: model.Failure{Kind: model.FailureInvalidInput, Message: message}, Cause: cause}
}
