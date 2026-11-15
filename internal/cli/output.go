// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/render"
)

const incomplete = "INCOMPLETE"

// validateDestination never writes. Invalid manifests cannot identify variant
// boundaries, so their failure reports require a destination outside Root.
func validateDestination(output, root string, spec model.Spec, loaded bool) error {
	if !filepath.IsAbs(output) || !filepath.IsAbs(root) {
		return errors.New("workspace and output must be absolute")
	}
	for p := root; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("inspect input workspace: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("input workspace is not an ordinary directory or traverses a symlink")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if !loaded {
		if within(root, output) {
			return errors.New("failure reports for an invalid manifest must be outside the input workspace")
		}
	} else {
		for _, v := range []model.Variant{spec.Variants.Baseline, spec.Variants.Candidate} {
			if within(filepath.Join(root, v.CWD), output) {
				return errors.New("output directory is inside a variant workspace")
			}
		}
	}
	if _, err := os.Lstat(output); err == nil {
		return errors.New("output directory already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output: %w", err)
	}
	// Every parent must already exist, be a directory, and not be a symlink.
	for p := filepath.Dir(output); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("inspect output parent: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("output parent is not an ordinary directory or traverses a symlink")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

type outputDir struct {
	root     *os.Root
	path     string
	identity os.FileInfo
}

func (o *outputDir) Close() error { return o.root.Close() }

func reserveOutput(path string) (*outputDir, error) {
	// Mkdir is exclusive: no existing data is removed or reused.
	if err := os.Mkdir(path, 0700); err != nil {
		return nil, fmt.Errorf("create new output: %w", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("output is incomplete (cannot open new directory): %w", err)
	}
	o := &outputDir{root: root, path: path}
	info, err := root.Stat(".")
	if err == nil {
		o.identity = info
	}
	if err == nil {
		err = o.atomic(incomplete, func(w io.Writer) error {
			_, e := io.WriteString(w, "Output persistence is incomplete. Do not treat these files as a completed report.\n")
			return e
		})
	}
	if err == nil {
		err = root.Mkdir("logs", 0700)
	}
	if err == nil {
		err = o.intact()
	}
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("output is incomplete: %w", err)
	}
	return o, nil
}

func (o *outputDir) intact() error {
	info, err := os.Lstat(o.path)
	if err != nil {
		return fmt.Errorf("output directory was removed or moved: %w", err)
	}
	if !info.IsDir() || !os.SameFile(info, o.identity) {
		return errors.New("output directory was replaced during execution")
	}
	return nil
}

// atomic writes a restricted temporary file inside the held root, then replaces
// its destination. Root confines symlinks/renames to the reserved output tree.
func (o *outputDir) atomic(name string, write func(io.Writer) error) (err error) {
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	temp := ".tmp-" + hex.EncodeToString(random[:])
	f, err := o.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		if err != nil {
			o.root.Remove(temp)
		}
	}()
	if err = write(f); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return o.root.Rename(temp, name)
}

func markIO(r *model.Report, err error) {
	if r.Outcome == model.OutcomeCancelled || r.Failure != nil && r.Failure.Kind == model.FailureCancelled {
		return
	}
	r.Outcome = model.OutcomeError
	r.Failure = &model.Failure{Kind: model.FailureIO, Message: "output persistence failed: " + err.Error()}
}

// persist updates log references only after actual writes, then writes both
// views of that same report. A failed write keeps the explicit marker and 2.
func persist(o *outputDir, r *model.Report) error {
	var first error
	record := func(err error) {
		if err != nil && first == nil {
			first = err
			markIO(r, err)
		}
	}
	record(o.intact())
	if first == nil {
		for i := range r.Variants {
			v := &r.Variants[i]
			for _, list := range [][]model.Check{v.Build, v.Tests} {
				for j := range list {
					c := &list[j]
					if c.Observation == nil {
						continue
					}
					for _, stream := range []struct {
						name    string
						capture *model.Capture
					}{{"stdout", &c.Observation.Stdout}, {"stderr", &c.Observation.Stderr}} {
						stream.capture.Log = ""
						name := fmt.Sprintf("logs/%s-%s-%d.%s", v.Name, c.Kind, c.Index, stream.name)
						err := o.atomic(name, func(w io.Writer) error { _, e := io.Copy(w, bytes.NewReader(stream.capture.Data)); return e })
						if err == nil {
							stream.capture.Log = name
						}
						record(err)
					}
				}
			}
		}
	}

	if err := o.reports(*r); err != nil {
		record(err)
		// Best effort: any writable report describes the persistence failure.
		_ = o.reports(*r)
	}
	if err := o.intact(); err != nil {
		record(err)
		_ = o.reports(*r)
	}
	if first == nil {
		if err := o.root.Remove(incomplete); err != nil {
			record(err)
			_ = o.reports(*r)
		}
	}
	if first != nil {
		return first
	}
	return nil
}

// reports replaces both views, retaining the first error while trying each sink.
func (o *outputDir) reports(r model.Report) error {
	var first error
	for _, view := range []struct {
		name  string
		write func(io.Writer, model.Report) error
	}{{"report.json", render.WriteJSON}, {"report.md", render.WriteMarkdown}} {
		if err := o.atomic(view.name, func(w io.Writer) error { return view.write(w, r) }); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// cancelledReports revises already-persisted evidence after a late interrupt.
// The marker is restored before replacement and remains on any rewrite failure.
func (o *outputDir) cancelledReports(r model.Report) error {
	var first error
	record := func(err error) {
		if first == nil && err != nil {
			first = err
		}
	}
	record(o.atomic(incomplete, func(w io.Writer) error {
		_, err := io.WriteString(w, "Cancellation report persistence is incomplete.\n")
		return err
	}))
	record(o.reports(r))
	record(o.intact())
	if first == nil {
		record(o.root.Remove(incomplete))
	}
	return first
}
