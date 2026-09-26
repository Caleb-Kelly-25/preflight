//go:build awsderive

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Caleb-Kelly-25/preflight/internal/derive"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
)

// writeEvidence records one run in mappings/evidence/<type>.json, merging with
// whatever is already there.
//
// The merge key is (operation, fixture). Re-running the same fixture REPLACES its
// previous record rather than appending a second one — a re-run against a newer
// provider supersedes the old measurement, and keeping both would leave the
// evidence saying two different things with nothing to choose between them.
//
// Different fixtures for the same operation are kept side by side, because they
// are genuinely different measurements: an update path depends on which attribute
// changed, and aws_iam_policy needs three fixtures to cover its update.
func writeEvidence(dir, resourceType, operation, fixture, providerVersion string, res derive.Result) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, resourceType+".json")

	ev := mapping.Evidence{ResourceType: resourceType}
	switch body, err := os.ReadFile(path); {
	case err == nil:
		if err := json.Unmarshal(body, &ev); err != nil {
			return "", fmt.Errorf("parsing the existing %s: %w", path, err)
		}
		// Guard against writing a merged file under the wrong name.
		if ev.ResourceType != resourceType {
			return "", fmt.Errorf("%s records resource_type %q, not %q", path, ev.ResourceType, resourceType)
		}
	case !os.IsNotExist(err):
		return "", fmt.Errorf("reading %s: %w", path, err)
	}

	run := mapping.Run{
		Operation: mapping.Operation(operation),
		// Recorded relative to the repository root and with forward slashes, so
		// the file reads the same whatever platform produced it.
		Fixture:         filepath.ToSlash(filepath.Clean(fixture)),
		DerivedAt:       today(),
		ProviderVersion: providerVersion,
		Sufficiency:     res.Sufficiency.String(),
		Minimality:      res.Minimal.String(),
		Attempts:        len(res.Attempts),
		Actions:         res.Sufficient,
	}
	if providerVersion == "" {
		// Better an explicit admission than a silently blank field: the whole
		// point of recording the version is that a derived set is only valid for
		// the provider that produced it.
		run.ProviderVersion = "unrecorded"
		run.Note = "provider version could not be read from the fixture's lock file"
	}

	replaced := false
	for i, existing := range ev.Runs {
		if existing.Operation == run.Operation && existing.Fixture == run.Fixture {
			// Carry forward a human-written note rather than discarding it; the
			// run's own note is only ever generated.
			if run.Note == "" {
				run.Note = existing.Note
			}
			ev.Runs[i] = run
			replaced = true
			break
		}
	}
	if !replaced {
		ev.Runs = append(ev.Runs, run)
	}

	body, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding evidence: %w", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// lockedProviderVersion pulls the AWS provider version out of a Terraform lock
// file. A derived set is only valid for the provider that produced it, so the
// version is part of the measurement rather than a detail.
var lockedProviderVersion = regexp.MustCompile(`(?s)provider\s+"registry\.terraform\.io/hashicorp/aws"\s*\{.*?version\s*=\s*"([^"]+)"`)

// providerVersion reads the version Terraform actually resolved, from the lock
// file it wrote into the staged working directory during init.
//
// Reading it from the lock file rather than asking the user is what makes the
// record trustworthy: the fixture does not pin a version, so the only authority
// on what ran is what Terraform selected.
func providerVersion(workDir string) string {
	body, err := os.ReadFile(filepath.Join(workDir, ".terraform.lock.hcl"))
	if err != nil {
		return ""
	}
	m := lockedProviderVersion.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// today is the run date in the YYYY-MM-DD form the evidence format uses.
func today() string { return time.Now().UTC().Format("2006-01-02") }
