package dicomwebcli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func writeJSON(stdout io.Writer, output string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if output == "" || output == "-" {
		_, err = stdout.Write(data)
		return err
	}
	return writeAtomicFile(output, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func preflightJSONOutput(output string) error {
	if output == "" || output == "-" {
		return nil
	}
	if _, err := os.Lstat(output); err == nil {
		return invalidInput("output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func ensureOutputDirectory(path string) error {
	if path == "" || path == "-" {
		return invalidInput("-output must name a directory")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return invalidInput("-output must name a directory")
	}
	return nil
}

func writeAtomicFile(path string, write func(io.Writer) error) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".dicomweb-output-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if err := write(temporary); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		temporaryOpen = false
		return err
	}
	temporaryOpen = false
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return invalidInput("output already exists")
		}
		return err
	}
	return nil
}

func removeCreated(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}
