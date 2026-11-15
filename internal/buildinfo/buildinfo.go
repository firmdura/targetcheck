// SPDX-License-Identifier: Apache-2.0

// Package buildinfo provides the same build identity to the CLI and reports.
package buildinfo

import (
	"runtime"
	"runtime/debug"

	"github.com/firmdura/targetcheck/internal/model"
)

// Version and Commit may be set at link time for reproducible release builds.
var Version = "dev"
var Commit = "unknown"

func Info() model.ToolVersion {
	version, commit := Version, Commit
	if info, ok := debug.ReadBuildInfo(); ok {
		const modulePath = "github.com/firmdura/targetcheck"
		if info.Main.Path == modulePath {
			if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
				version = info.Main.Version
			}
			if commit == "unknown" {
				for _, setting := range info.Settings {
					if setting.Key == "vcs.revision" {
						commit = setting.Value
					}
				}
			}
		} else if version == "dev" {
			// A library caller's main VCS revision identifies the caller, not this tool.
			// Use the dependency version when available and leave our commit unknown.
			for _, dependency := range info.Deps {
				if dependency.Path == modulePath && dependency.Replace == nil && dependency.Version != "" {
					version = dependency.Version
					break
				}
			}
		}
	}
	return model.ToolVersion{Version: version, Commit: commit, GoVersion: runtime.Version()}
}
