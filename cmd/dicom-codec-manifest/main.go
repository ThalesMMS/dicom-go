package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecprofile"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("dicom-codec-manifest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	validateOnly := flags.Bool("validate-only", false, "validate the manifest without writing JSON")
	requireReady := flags.Bool("require-ready", false, "fail unless every clinical release gate is satisfied")
	evidenceRoot := flags.String("evidence-root", "", "dicom-go module root containing codecfull release evidence")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}

	manifest := codecprofile.CodecFullManifest()
	if err := manifest.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *requireReady {
		if err := manifest.ValidateForRelease(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		root, err := resolveEvidenceRoot(*evidenceRoot)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := codecfixture.ValidateCodecFullReleaseEvidence(root); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := validateManifestEvidence(root, manifest); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *validateOnly {
		return 0
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(manifest); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func resolveEvidenceRoot(explicit string) (string, error) {
	if explicit != "" {
		root, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("resolve codecfull evidence root: %w", err)
		}
		if err := validateModuleRoot(root); err != nil {
			return "", err
		}
		return root, nil
	}
	current, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	for {
		if validateModuleRoot(current) == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("codecfull evidence root not found from current directory")
		}
		current = parent
	}
}

func validateModuleRoot(root string) error {
	file, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return fmt.Errorf("codecfull evidence root %q: %w", root, err)
	}
	defer file.Close()

	const expectedModule = "github.com/ThalesMMS/dicom-go"
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "module" {
			continue
		}
		if len(fields) == 2 && fields[1] == expectedModule {
			return nil
		}
		return fmt.Errorf("codecfull evidence root %q is not the dicom-go module", root)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("codecfull evidence root %q: read go.mod: %w", root, err)
	}
	return fmt.Errorf("codecfull evidence root %q is not the dicom-go module", root)
}

func validateManifestEvidence(root string, manifest codecprofile.ProfileManifest) error {
	for _, capability := range manifest.Capabilities {
		for _, evidence := range capability.Evidence {
			path := filepath.Join(root, filepath.FromSlash(evidence.Path))
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("codecfull capability %s evidence %q: %w", capability.ID, evidence.Path, err)
			}
			for _, test := range evidence.Tests {
				declaration := []byte("func " + strings.TrimSpace(test) + "(")
				if !bytes.Contains(data, declaration) {
					return fmt.Errorf("codecfull capability %s evidence %q does not declare %s", capability.ID, evidence.Path, test)
				}
			}
		}
	}
	for _, performance := range manifest.PerformanceEvidence {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(performance.Path))); err != nil {
			return fmt.Errorf("codecfull performance evidence %q: %w", performance.Path, err)
		}
	}
	return nil
}
