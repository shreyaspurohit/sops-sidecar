// Package health owns readiness: the ready marker in the output dir.
package health

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/shreyaspurohit/sops-sidecar/internal/writer"
)

// MarkerName is the file name of the ready marker.
const MarkerName = writer.ReservedPrefix + "-ready"

// MarkerMode is the mode of the ready marker, readable by every neighbor.
const MarkerMode writer.Mode = 0o444

// Marker sets and clears the ready marker in one output dir. The marker
// exists only while the last load succeeded.
type Marker struct {
	dir  *writer.Dir
	name writer.Name
}

// NewMarker returns the marker of the output dir d.
func NewMarker(d *writer.Dir) (Marker, error) {
	name, err := writer.ParseReservedName(MarkerName)
	if err != nil {
		return Marker{}, fmt.Errorf("ready marker name: %w", err)
	}
	return Marker{dir: d, name: name}, nil
}

// Set atomically writes an empty marker file, leaving its owner unchanged.
func (m Marker) Set() error {
	err := m.dir.Write(writer.File{
		Name:  m.name,
		Mode:  MarkerMode,
		Owner: writer.KeepOwner(),
	})
	if err != nil {
		return fmt.Errorf("set ready marker: %w", err)
	}
	return nil
}

// Clear removes the marker. It succeeds when the marker is already absent.
func (m Marker) Clear() error {
	if err := m.dir.Remove(m.name); err != nil {
		return fmt.Errorf("clear ready marker: %w", err)
	}
	return nil
}

// Check returns nil only when outputDir holds the marker as a regular file.
// A symlink does not count as a marker.
func Check(outputDir string) error {
	info, err := os.Lstat(filepath.Join(outputDir, MarkerName))
	if err != nil {
		return fmt.Errorf("not ready: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not ready: %s is not a regular file", MarkerName)
	}
	return nil
}
