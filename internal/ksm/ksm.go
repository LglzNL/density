package ksm

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultPath ist der typische sysfs-Pfad für KSM Controls/Stats.
const DefaultPath = "/sys/kernel/mm/ksm"

// Config beschreibt die wichtigsten KSM-Tuning-Parameter, die DENSITY verwaltet.
//
// MergeAcrossNodes = -1 bedeutet "nicht ändern".
type Config struct {
	Path             string
	PagesToScan      int
	SleepMillisecs   int
	MergeAcrossNodes int // -1 = keep current
}

func (c Config) normalized() Config {
	out := c
	if out.Path == "" {
		out.Path = DefaultPath
	}
	return out
}

// Enable setzt Tuning-Werte (wenn möglich) und startet KSM (run=1).
// Wenn dryRun=true, werden keine Writes durchgeführt.
func Enable(cfg Config, dryRun bool) error {
	cfg = cfg.normalized()

	if cfg.PagesToScan <= 0 {
		return fmt.Errorf("pages_to_scan must be > 0")
	}
	if cfg.SleepMillisecs < 0 {
		return fmt.Errorf("sleep_millisecs must be >= 0")
	}

	if dryRun {
		return nil
	}

	// Erst tunen, dann starten.
	if err := writeInt(filepath.Join(cfg.Path, "pages_to_scan"), int64(cfg.PagesToScan)); err != nil {
		return err
	}
	if err := writeInt(filepath.Join(cfg.Path, "sleep_millisecs"), int64(cfg.SleepMillisecs)); err != nil {
		return err
	}
	if cfg.MergeAcrossNodes >= 0 {
		if err := writeInt(filepath.Join(cfg.Path, "merge_across_nodes"), int64(cfg.MergeAcrossNodes)); err != nil {
			return err
		}
	}
	if err := writeInt(filepath.Join(cfg.Path, "run"), 1); err != nil {
		return err
	}

	return nil
}

// Disable stoppt KSM.
// Wenn unmerge=true, wird run=2 gesetzt und best-effort bis pages_shared=0 gewartet (oder timeout).
func Disable(path string, unmerge bool, timeout time.Duration, dryRun bool) error {
	if path == "" {
		path = DefaultPath
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	if dryRun {
		return nil
	}

	if unmerge {
		if err := writeInt(filepath.Join(path, "run"), 2); err != nil {
			return err
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			shared, err := readInt(filepath.Join(path, "pages_shared"))
			if err == nil && shared == 0 {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		return writeInt(filepath.Join(path, "run"), 0)
	}

	return writeInt(filepath.Join(path, "run"), 0)
}

// Status liest alle numerischen Dateien im KSM-sysfs-Verzeichnis aus.
// Nicht-numerische Files/Dirs werden ignoriert.
func Status(path string) (map[string]int64, error) {
	if path == "" {
		path = DefaultPath
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	out := make(map[string]int64)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		val, err := readInt(filepath.Join(path, name))
		if err != nil {
			continue
		}
		out[name] = val
	}

	if len(out) == 0 {
		return out, errors.New("keine numerischen KSM-Felder gefunden (unterstützt der Kernel KSM?)")
	}
	return out, nil
}

// ReadInt liest ein numerisches sysfs-File unterhalb des KSM-Pfads.
func ReadInt(path, name string) (int64, error) {
	if path == "" {
		path = DefaultPath
	}
	return readInt(filepath.Join(path, name))
}

// WriteInt schreibt ein numerisches sysfs-File unterhalb des KSM-Pfads.
func WriteInt(path, name string, v int64) error {
	if path == "" {
		path = DefaultPath
	}
	return writeInt(filepath.Join(path, name), v)
}

func readInt(p string) (int64, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	return strconv.ParseInt(s, 10, 64)
}

func writeInt(p string, v int64) error {
	// sysfs existiert bereits; perms sind irrelevant, aber müssen gesetzt sein für WriteFile.
	return os.WriteFile(p, []byte(strconv.FormatInt(v, 10)), fs.FileMode(0o644))
}

// ReadMemInfo liest ausgewählte Felder aus /proc/meminfo.
// Werte sind in kB.
func ReadMemInfo() (map[string]uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	want := map[string]bool{
		"MemTotal":     true,
		"MemFree":      true,
		"MemAvailable": true,
		"SwapTotal":    true,
		"SwapFree":     true,
	}

	out := make(map[string]uint64)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		if !want[key] {
			continue
		}
		val, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
