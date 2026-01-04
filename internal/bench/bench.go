package bench

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LglzNL/density/internal/ksm"
)

type Profile string

const (
	ProfileP1 Profile = "P1" // identisch (maximaler KSM-Effekt)
	ProfileP2 Profile = "P2" // ähnlich (realistischer)
	ProfileP3 Profile = "P3" // worst case (divergent)
)

type Config struct {
	ExecPath string
	OutDir   string

	KSMPath string

	Profile   Profile
	Instances []int

	MemMiB   int
	Warmup   time.Duration
	Interval time.Duration // redirty interval for P3; optional
}

type StepResult struct {
	N          int           `json:"n"`
	Alive      int           `json:"alive"`
	Duration   time.Duration `json:"duration"`
	Profile    Profile       `json:"profile"`
	MemMiB     int           `json:"mem_mib"`
	Warmup     time.Duration `json:"warmup"`

	PreMemKB  map[string]uint64 `json:"pre_mem_kb,omitempty"`
	PostMemKB map[string]uint64 `json:"post_mem_kb,omitempty"`

	PreKSM  map[string]int64 `json:"pre_ksm,omitempty"`
	PostKSM map[string]int64 `json:"post_ksm,omitempty"`

	EstimatedSavedMiB float64 `json:"estimated_saved_mib"`
	KsmdTicksDelta    int64   `json:"ksmd_ticks_delta,omitempty"`

	Notes string `json:"notes,omitempty"`
}

type RunResult struct {
	StartedAt time.Time    `json:"started_at"`
	Profile   Profile      `json:"profile"`
	Steps     []StepResult `json:"steps"`
}

func Run(ctx context.Context, cfg Config) (*RunResult, error) {
	if cfg.ExecPath == "" {
		return nil, errors.New("ExecPath fehlt (Pfad zum densityctl binary)")
	}
	if cfg.OutDir == "" {
		cfg.OutDir = "results"
	}
	if cfg.KSMPath == "" {
		cfg.KSMPath = ksm.DefaultPath
	}
	if cfg.MemMiB <= 0 {
		cfg.MemMiB = 256
	}
	if cfg.Warmup <= 0 {
		cfg.Warmup = 20 * time.Second
	}
	if cfg.Profile == "" {
		cfg.Profile = ProfileP1
	}
	if len(cfg.Instances) == 0 {
		return nil, errors.New("Instances ist leer")
	}

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return nil, err
	}

	res := &RunResult{
		StartedAt: time.Now(),
		Profile:   cfg.Profile,
	}

	for _, n := range cfg.Instances {
		if n <= 0 {
			continue
		}

		step := StepResult{
			N:       n,
			Profile: cfg.Profile,
			MemMiB:  cfg.MemMiB,
			Warmup:  cfg.Warmup,
		}

		preMem, _ := ksm.ReadMemInfo()
		preK, _ := ksm.Status(cfg.KSMPath)
		step.PreMemKB = preMem
		step.PreKSM = preK

		ksmdBefore, _ := readKsmdTicks()

		cmds, err := startHogs(ctx, cfg, n)
		if err != nil {
			step.Notes = "Startfehler: " + err.Error()
			res.Steps = append(res.Steps, step)
			continue
		}

		// Warmup – KSM braucht Zeit zum Scannen/Mergen.
		select {
		case <-ctx.Done():
			_ = stopHogs(cmds)
			return res, ctx.Err()
		case <-time.After(cfg.Warmup):
		}

		alive := countAlive(cmds)
		step.Alive = alive

		postMem, _ := ksm.ReadMemInfo()
		postK, _ := ksm.Status(cfg.KSMPath)
		step.PostMemKB = postMem
		step.PostKSM = postK

		ksmdAfter, _ := readKsmdTicks()
		if ksmdBefore > 0 && ksmdAfter > 0 && ksmdAfter >= ksmdBefore {
			step.KsmdTicksDelta = ksmdAfter - ksmdBefore
		}

		step.EstimatedSavedMiB = estimateSavedMiB(postK)

		// Cleanup
		_ = stopHogs(cmds)

		step.Duration = cfg.Warmup
		res.Steps = append(res.Steps, step)
	}

	// Write JSON
	jPath := filepath.Join(cfg.OutDir, fmt.Sprintf("bench_%s_%s.json", strings.ToLower(string(cfg.Profile)), time.Now().Format("20060102_150405")))
	if err := writeJSON(jPath, res); err != nil {
		return res, err
	}

	// Write Markdown summary
	mdPath := filepath.Join(cfg.OutDir, "report.md")
	_ = os.WriteFile(mdPath, []byte(renderMarkdown(res)), 0o644)

	return res, nil
}

func startHogs(ctx context.Context, cfg Config, n int) ([]*exec.Cmd, error) {
	cmds := make([]*exec.Cmd, 0, n)

	// Profile -> dirty behavior:
	// P1: 0% unique, no redirty
	// P2: 5% unique
	// P3: 50% unique + periodic re-dirty (1s default)
	var dirtyPct float64
	var redirty time.Duration
	switch cfg.Profile {
	case ProfileP1:
		dirtyPct = 0
		redirty = 0
	case ProfileP2:
		dirtyPct = 5
		redirty = 0
	case ProfileP3:
		dirtyPct = 50
		if cfg.Interval > 0 {
			redirty = cfg.Interval
		} else {
			redirty = 1 * time.Second
		}
	default:
		dirtyPct = 0
		redirty = 0
	}

	for i := 0; i < n; i++ {
		cmd := exec.CommandContext(ctx, cfg.ExecPath,
			"__hog",
			"--mem-mib", strconv.Itoa(cfg.MemMiB),
			"--id", strconv.Itoa(i),
			"--dirty-pct", fmt.Sprintf("%.2f", dirtyPct),
			"--redirty-ms", strconv.Itoa(int(redirty.Milliseconds())),
		)
		cmd.Stdout = nil
		cmd.Stderr = nil

		// Start
		if err := cmd.Start(); err != nil {
			// Stop already started ones
			_ = stopHogs(cmds)
			return nil, err
		}
		cmds = append(cmds, cmd)
	}

	return cmds, nil
}

func stopHogs(cmds []*exec.Cmd) error {
	// Try SIGTERM, then SIGKILL.
	for _, c := range cmds {
		if c == nil || c.Process == nil {
			continue
		}
		_ = c.Process.Signal(syscall.SIGTERM)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		allDone := true
		for _, c := range cmds {
			if c == nil {
				continue
			}
			if c.ProcessState != nil && c.ProcessState.Exited() {
				continue
			}
			// Poll wait non-blocking not possible; use Process.Signal 0
			if c.Process != nil {
				if err := c.Process.Signal(syscall.Signal(0)); err == nil {
					allDone = false
				}
			}
		}
		if allDone {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	for _, c := range cmds {
		if c == nil || c.Process == nil {
			continue
		}
		_ = c.Process.Kill()
		_, _ = c.Process.Wait()
	}
	return nil
}

func countAlive(cmds []*exec.Cmd) int {
	alive := 0
	for _, c := range cmds {
		if c == nil || c.Process == nil {
			continue
		}
		if err := c.Process.Signal(syscall.Signal(0)); err == nil {
			alive++
		}
	}
	return alive
}

func estimateSavedMiB(ksmStats map[string]int64) float64 {
	if ksmStats == nil {
		return 0
	}
	ps := os.Getpagesize()
	shared := ksmStats["pages_shared"]
	sharing := ksmStats["pages_sharing"]
	if shared <= 0 || sharing <= 0 || sharing < shared {
		return 0
	}
	savedPages := sharing - shared
	savedBytes := float64(savedPages) * float64(ps)
	return savedBytes / (1024.0 * 1024.0)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func renderMarkdown(r *RunResult) string {
	var b strings.Builder
	b.WriteString("# DENSITY Bench Report\n\n")
	b.WriteString(fmt.Sprintf("- Zeitpunkt: %s\n", r.StartedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- Profil: %s\n\n", r.Profile))

	b.WriteString("| N | Alive | Saved (MiB) | ksmd ticks Δ | MemAvailable vorher (MiB) | MemAvailable nachher (MiB) |\n")
	b.WriteString("|---:|---:|---:|---:|---:|---:|\n")
	for _, s := range r.Steps {
		preAvail := memMiB(s.PreMemKB, "MemAvailable")
		postAvail := memMiB(s.PostMemKB, "MemAvailable")
		b.WriteString(fmt.Sprintf("| %d | %d | %.1f | %d | %.1f | %.1f |\n",
			s.N, s.Alive, s.EstimatedSavedMiB, s.KsmdTicksDelta, preAvail, postAvail))
	}
	b.WriteString("\n")
	b.WriteString("**Hinweis:** Der geschätzte \"Saved\"-Wert basiert auf KSM-Statistiken (pages_sharing/pages_shared) und ist workload-abhängig.\n")
	return b.String()
}

func memMiB(m map[string]uint64, key string) float64 {
	if m == nil {
		return 0
	}
	kb := m[key]
	return float64(kb) / 1024.0
}

func readKsmdTicks() (int64, error) {
	pid, err := findPIDByComm("ksmd")
	if err != nil {
		return 0, err
	}
	statPath := filepath.Join("/proc", strconv.Itoa(pid), "stat")
	b, err := os.ReadFile(statPath)
	if err != nil {
		return 0, err
	}
	// /proc/[pid]/stat: fields are space-separated, but field 2 can contain spaces in parentheses.
	// We'll parse carefully.
	utime, stime, err := parseProcStatUtimeStime(string(b))
	if err != nil {
		return 0, err
	}
	return utime + stime, nil
}

func findPIDByComm(comm string) (int, error) {
	d, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, e := range d {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cpath := filepath.Join("/proc", e.Name(), "comm")
		b, err := os.ReadFile(cpath)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(b))
		if name == comm {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("process %q nicht gefunden", comm)
}

func parseProcStatUtimeStime(stat string) (int64, int64, error) {
	// Find the last ')' which ends comm field.
	i := strings.LastIndex(stat, ")")
	if i < 0 {
		return 0, 0, fmt.Errorf("unexpected /proc/stat format")
	}
	after := strings.Fields(stat[i+1:])
	// utime is field 14, stime field 15 in the original format.
	// After stripping pid+comm, the offset changes.
	// Original fields:
	// 1 pid, 2 comm, 3 state, 4 ppid, ... 14 utime, 15 stime
	// After comm removed, after[0] = state (field 3).
	// Thus utime (14) -> after index (14-3) = 11, stime -> 12
	if len(after) < 13 {
		return 0, 0, fmt.Errorf("unexpected /proc/stat fields")
	}
	ut, err := strconv.ParseInt(after[11], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	st, err := strconv.ParseInt(after[12], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return ut, st, nil
}

// Optional: read simple vmstat counters (pswpin/pswpout).
func ReadVMStat() (map[string]uint64, error) {
	f, err := os.Open("/proc/vmstat")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	want := map[string]bool{
		"pswpin":  true,
		"pswpout": true,
	}

	out := make(map[string]uint64)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) != 2 {
			continue
		}
		if !want[parts[0]] {
			continue
		}
		v, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		out[parts[0]] = v
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
