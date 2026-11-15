// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/pkg/targetcheck"
)

func TestSchemaFixtureCatalog(t *testing.T) {
	root := filepath.Join("..", "..")
	var catalog []struct {
		File, Schema string
		Valid        bool
	}
	readJSON(t, filepath.Join(root, "testdata", "contracts", "catalog.json"), &catalog)
	if len(catalog) < 10 {
		t.Fatal("fixture catalog unexpectedly empty")
	}
	for _, entry := range catalog {
		t.Run(entry.File, func(t *testing.T) {
			var schema map[string]any
			var value any
			readJSON(t, filepath.Join(root, "schema", entry.Schema), &schema)
			readJSON(t, filepath.Join(root, "testdata", "contracts", entry.File), &value)
			err := validateSchema(schema, schema, value)
			if (err == nil) != entry.Valid {
				t.Fatalf("valid=%v, schema result: %v", entry.Valid, err)
			}
		})
	}
}

// A deliberately small, generic JSON Schema vocabulary interpreter keeps
// contract fixture checks offline and independent of the future manifest loader.
// Every validation keyword used by these schemas is covered below; unsupported
// keywords fail the test instead of being silently ignored.
func validateSchema(root, schema map[string]any, value any) error {
	allowed := "$schema $id $defs title description $ref type const enum allOf anyOf oneOf if then not properties required additionalProperties propertyNames items prefixItems minItems maxItems minLength pattern minimum maximum"
	for k := range schema {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			return fmt.Errorf("unsupported schema keyword %q", k)
		}
	}
	if r, ok := schema["$ref"].(string); ok {
		if !strings.HasPrefix(r, "#/$defs/") {
			return fmt.Errorf("unsupported reference %q", r)
		}
		defs, ok := root["$defs"].(map[string]any)
		if !ok {
			return errors.New("missing definitions")
		}
		node, ok := defs[strings.TrimPrefix(r, "#/$defs/")].(map[string]any)
		if !ok {
			return fmt.Errorf("missing reference %q", r)
		}
		if err := validateSchema(root, node, value); err != nil {
			return err
		}
	}
	if c, ok := schema["const"]; ok && !reflect.DeepEqual(c, value) {
		return fmt.Errorf("expected constant %v", c)
	}
	if e, ok := schema["enum"].([]any); ok {
		found := false
		for _, c := range e {
			found = found || reflect.DeepEqual(c, value)
		}
		if !found {
			return fmt.Errorf("value outside enum: %v", value)
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if nodes, ok := schema[keyword].([]any); ok {
			n := 0
			for _, node := range nodes {
				if validateSchema(root, node.(map[string]any), value) == nil {
					n++
				}
			}
			if (keyword == "allOf" && n != len(nodes)) || (keyword == "anyOf" && n == 0) || (keyword == "oneOf" && n != 1) {
				return fmt.Errorf("%s: %d alternatives matched", keyword, n)
			}
		}
	}
	if node, ok := schema["not"].(map[string]any); ok && validateSchema(root, node, value) == nil {
		return errors.New("forbidden shape")
	}
	if node, ok := schema["if"].(map[string]any); ok && validateSchema(root, node, value) == nil {
		if then, ok := schema["then"].(map[string]any); ok {
			if err := validateSchema(root, then, value); err != nil {
				return err
			}
		}
	}
	if typ, ok := schema["type"].(string); ok {
		valid := false
		switch typ {
		case "object":
			_, valid = value.(map[string]any)
		case "array":
			_, valid = value.([]any)
		case "string":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "integer":
			if n, ok := value.(float64); ok {
				valid = n == float64(int64(n))
			}
		default:
			return fmt.Errorf("unsupported schema type %q", typ)
		}
		if !valid {
			return fmt.Errorf("expected %s, got %T", typ, value)
		}
	}
	switch v := value.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if _, ok := v[key.(string)]; !ok {
					return fmt.Errorf("missing %s", key)
				}
			}
		}
		for key, child := range v {
			if ns, ok := schema["propertyNames"].(map[string]any); ok {
				if err := validateSchema(root, ns, key); err != nil {
					return err
				}
			}
			if node, ok := props[key].(map[string]any); ok {
				if err := validateSchema(root, node, child); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				continue
			}
			switch ap := schema["additionalProperties"].(type) {
			case bool:
				if !ap {
					return fmt.Errorf("unknown property %s", key)
				}
			case map[string]any:
				if err := validateSchema(root, ap, child); err != nil {
					return err
				}
			}
		}
	case []any:
		if n, ok := schema["minItems"].(float64); ok && len(v) < int(n) {
			return errors.New("too few items")
		}
		if n, ok := schema["maxItems"].(float64); ok && len(v) > int(n) {
			return errors.New("too many items")
		}
		prefix, _ := schema["prefixItems"].([]any)
		for i, child := range v {
			var node map[string]any
			if i < len(prefix) {
				node = prefix[i].(map[string]any)
			} else {
				node, _ = schema["items"].(map[string]any)
			}
			if node != nil {
				if err := validateSchema(root, node, child); err != nil {
					return fmt.Errorf("item %d: %w", i, err)
				}
			}
		}
	case string:
		if n, ok := schema["minLength"].(float64); ok && utf8.RuneCountInString(v) < int(n) {
			return errors.New("string too short")
		}
		if pattern, ok := schema["pattern"].(string); ok {
			// JSON Schema uses ECMA-262 lookahead for lexical path constraints.
			// Go regexp has no lookahead; implement precisely this one anchored
			// lexical rule here, leaving filesystem/symlink checks to the planner.
			if strings.HasPrefix(pattern, "^(?!/)") {
				if strings.HasPrefix(v, "/") || strings.ContainsRune(v, 0) {
					return errors.New("invalid relative path")
				}
				for _, part := range strings.Split(v, "/") {
					if part == ".." {
						return errors.New("parent traversal")
					}
				}
			} else {
				r, err := regexp.Compile(strings.ReplaceAll(pattern, `\u0000`, `\x00`))
				if err != nil {
					return fmt.Errorf("invalid schema regex: %w", err)
				}
				if !r.MatchString(v) {
					return errors.New("string pattern mismatch")
				}
			}
		}
	case float64:
		if n, ok := schema["minimum"].(float64); ok && v < n {
			return errors.New("below minimum")
		}
		if n, ok := schema["maximum"].(float64); ok && v > n {
			return errors.New("above maximum")
		}
	}
	return nil
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func TestPublicWireTypesRoundTrip(t *testing.T) {
	for _, name := range []string{"native.json", "arm.json", "report-matched.json", "report-partial.json"} {
		t.Run(name, func(t *testing.T) {
			var original any
			path := filepath.Join("..", "..", "testdata", "contracts", "valid", name)
			readJSON(t, path, &original)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var typed any
			if strings.HasPrefix(name, "report-") {
				typed = &targetcheck.Report{}
			} else {
				typed = &targetcheck.Spec{}
			}
			if err = json.Unmarshal(data, typed); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(typed)
			if err != nil {
				t.Fatal(err)
			}
			var got any
			if err = json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, got) {
				t.Fatalf("public wire fields lost in round trip:\n%s", encoded)
			}
		})
	}
}

func TestExecutionSecretsAndRawCapturesStayOutOfJSON(t *testing.T) {
	secret := "private-env-value-and-raw-command-output"
	data, err := json.Marshal(model.ProcessObservation{EnvNames: []string{"TOKEN"}, Stdout: model.Capture{Data: []byte(secret)}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatalf("raw capture exposed: %s", data)
	}
	data, err = json.Marshal(model.CommandRequest{Env: map[string]string{"TOKEN": secret}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatalf("environment value exposed: %s", data)
	}
	e := &targetcheck.RunError{Kind: targetcheck.ErrorCancelled, Failure: targetcheck.Failure{Kind: targetcheck.FailureCancelled, Message: "cancelled"}, Cause: context.Canceled}
	if !errors.Is(e, context.Canceled) {
		t.Fatal("typed error lost cancellation cause")
	}
	data, err = json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("Cause")) {
		t.Fatal("error cause exposed")
	}
}

// Error names the category, the specific failure kind and the message once.
func TestRunErrorStatesCategoryKindAndMessageOnce(t *testing.T) {
	for _, c := range []struct {
		err  *model.RunError
		want string
	}{
		{&model.RunError{Kind: model.ErrorInvalidInput, Failure: model.Failure{Kind: model.FailureMissingTool, Message: "QEMU executable is unavailable"}}, "invalid_input: missing_tool: QEMU executable is unavailable"},
		{&model.RunError{Kind: model.ErrorInvalidInput, Failure: model.Failure{Kind: model.FailureInvalidInput, Message: "Root must be an explicit absolute directory"}}, "invalid_input: Root must be an explicit absolute directory"},
		{&model.RunError{Kind: model.ErrorInfrastructure, Failure: model.Failure{Kind: model.FailureTimeout, Message: `baseline test "t2": command exceeded its configured timeout`}}, `infrastructure: timeout: baseline test "t2": command exceeded its configured timeout`},
		{&model.RunError{Kind: model.ErrorInfrastructure, Failure: model.Failure{Kind: model.FailureIO, Message: "create new output: file exists"}}, "infrastructure: io: create new output: file exists"},
		{&model.RunError{Kind: model.ErrorCancelled, Failure: model.Failure{Kind: model.FailureCancelled, Message: "run cancelled by caller"}}, "cancelled: run cancelled by caller"},
		{&model.RunError{Kind: model.ErrorInvalidInput, Failure: model.Failure{Message: "unclassified"}}, "invalid_input: unclassified"},
		{nil, "<nil>"},
	} {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
}

func TestNormalExit127DiffersFromLaunchFailure(t *testing.T) {
	exit := 127
	normal := model.ProcessObservation{Started: true, ExitCode: &exit}
	launch := model.ProcessObservation{Failure: &model.Failure{Kind: model.FailureLaunch, Message: "executable missing"}}
	if !normal.Started || normal.ExitCode == nil || normal.Failure != nil {
		t.Fatal("normal exit is ambiguous")
	}
	if launch.Started || launch.ExitCode != nil || launch.Failure.Kind != model.FailureLaunch {
		t.Fatal("launch failure is ambiguous")
	}
}
