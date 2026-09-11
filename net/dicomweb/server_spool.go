package dicomweb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type dicomwebSpoolEntry struct {
	name    string
	size    int64
	modTime time.Time
	removed bool
}

func scavengeDICOMwebSpool(options ServerOptions, limits ServerLimits, now time.Time) error {
	return scavengeDICOMwebSpoolWithRemove(options, limits, now, os.Remove)
}

func scavengeDICOMwebSpoolWithRemove(options ServerOptions, limits ServerLimits, now time.Time, removeFile func(string) error) error {
	directory := options.SpoolDirectory
	if directory == "" {
		directory = os.TempDir()
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: spool directory is unavailable", ErrInvalidServerOptions)
	}
	owned := make([]dicomwebSpoolEntry, 0)
	var aggregate int64
	for _, entry := range entries {
		if !dicomwebOwnsSpoolName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() < 0 || aggregate > int64(^uint64(0)>>1)-info.Size() {
			continue
		}
		aggregate += info.Size()
		owned = append(owned, dicomwebSpoolEntry{name: entry.Name(), size: info.Size(), modTime: info.ModTime()})
	}
	activeCutoff := now.Add(-limits.MaxDuration)
	retentionCutoff := now.Add(-options.SpoolRetentionAge)
	removeEntry := func(entry *dicomwebSpoolEntry) error {
		path := filepath.Join(directory, entry.name)
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			entry.removed = true
			return nil
		}
		if statErr != nil || !info.Mode().IsRegular() {
			// Racing or irregular entries are skipped so scavenging can continue.
			return nil
		}
		if err := removeFile(path); err != nil {
			// Individual removal failures are non-fatal and can be retried later.
			return nil
		}
		entry.removed = true
		aggregate -= entry.size
		return nil
	}
	for index := range owned {
		entry := &owned[index]
		if entry.modTime.Before(activeCutoff) && entry.modTime.Before(retentionCutoff) {
			if err := removeEntry(entry); err != nil {
				return err
			}
		}
	}
	if aggregate <= options.SpoolAggregateQuotaBytes {
		return nil
	}
	sort.SliceStable(owned, func(left, right int) bool { return owned[left].modTime.Before(owned[right].modTime) })
	for index := range owned {
		entry := &owned[index]
		if aggregate <= options.SpoolAggregateQuotaBytes {
			break
		}
		if entry.removed || !entry.modTime.Before(activeCutoff) {
			continue
		}
		if err := removeEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

func dicomwebOwnsSpoolName(name string) bool {
	for _, prefix := range []string{".dicomweb-json-", ".dicomweb-stow-", ".dicomweb-render-"} {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			return true
		}
	}
	return false
}
