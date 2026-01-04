package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LglzNL/density/internal/bench"
	"github.com/LglzNL/density/internal/ksm"
)

const (
	projectName = "DENSITY"
)

// DENSITY ist sowohl Produkt als auch (im MVP) der "Algorithmus"/Policy-Layer:
// - Produkt: CLI + Benchmarks + Website + Distribution
// - Algorithmus: konservative, transparente Tuning-Policy (KSM + Messung), ohne Kernel-Module.
func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "help", "-h", "--help":
		usage()
		return
	case "enable":
		err = cmdEnable(args)
	case "disable":
		err = cmdDisable(args)
	case "status":
		err = cmdStatus(args)
	case "bench":
		err = cmdBench(args)
	case "__hog":
		// Internes Subcommand für Benchmarks (nicht dokumentiert für Endnutzer).
		err = cmdHog(args)
	default:
		fmt.Fprintf(os.Stderr, "Unbekannter Befehl: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Fehler: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Printf(`%s – Produkt & Algorithmus (MVP)
====================================

Ziel: Mehr Instanzen pro Host, indem RAM-Druck reduziert wird (Linux/KSM).
Transparente Metriken. Reproduzierbare Benchmarks. Jederzeit revertierbar.

Befehle:
  enable     KSM aktivieren (konservative Defaults, optional anpassen)
  disable    KSM deaktivieren (optional: unmerge)
  status     KSM-Status/Stats anzeigen
  bench      reproduzierbarer Benchmark (P1–P3)

Hinweis:
  Dieses MVP nutzt ausschließlich standardisierte Kernel-Interfaces (sysfs).
  Keine eigenen Kernel-Module.

Beispiele:
  sudo densityctl enable
  densityctl status
  sudo densityctl bench --profile P1 --scale 10..80 --mem-mib 256 --out results --publish docs/data/benchmarks.latest.json

`, projectName)
}

func cmdEnable(args []string) error {
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	var (
		ksmPath   = fs.String("ksm-path", ksm.DefaultPath, "KSM sysfs Pfad (default: /sys/kernel/mm/ksm)")
		pagesScan = fs.Int("pages-to-scan", 100, "KSM: pages_to_scan (konservativ: 100)")
		sleepMs   = fs.Int("sleep-ms", 20, "KSM: sleep_millisecs (konservativ: 20)")
		mergeAN   = fs.Int("merge-across-nodes", -1, "KSM: merge_across_nodes (0/1). -1 = nicht ändern")
		dryRun    = fs.Bool("dry-run", false, "Nur anzeigen, nichts schreiben")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *dryRun {
		fmt.Printf("[dry-run] würde KSM aktivieren: pages_to_scan=%d sleep_ms=%d merge_across_nodes=%d path=%s\n",
			*pagesScan, *sleepMs, *mergeAN, *ksmPath)
		return nil
	}

	cfg := ksm.Config{
		Path:             *ksmPath,
		PagesToScan:      *pagesScan,
		SleepMillisecs:   *sleepMs,
		MergeAcrossNodes: *mergeAN,
	}

	if err := ksm.Enable(cfg, false); err != nil {
		return err
	}

	fmt.Println("OK: KSM ist aktiv (run=1).")
	return nil
}

func cmdDisable(args []string) error {
	fs := flag.NewFlagSet("disable", flag.ContinueOnError)
	var (
		ksmPath  = fs.String("ksm-path", ksm.DefaultPath, "KSM sysfs Pfad")
		unmerge  = fs.Bool("unmerge", true, "run=2 (unmerge) und warten bis pages_shared=0 (best-effort)")
		timeoutS = fs.Int("timeout-sec", 60, "Timeout in Sekunden für unmerge-wait")
		dryRun   = fs.Bool("dry-run", false, "Nur anzeigen, nichts schreiben")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *dryRun {
		fmt.Printf("[dry-run] würde KSM deaktivieren: unmerge=%v timeout=%ds path=%s\n", *unmerge, *timeoutS, *ksmPath)
		return nil
	}

	if err := ksm.Disable(*ksmPath, *unmerge, time.Duration(*timeoutS)*time.Second, false); err != nil {
		return err
	}
	fmt.Println("OK: KSM ist deaktiviert (run=0).")
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var (
		ksmPath = fs.String("ksm-path", ksm.DefaultPath, "KSM sysfs Pfad")
		asJSON  = fs.Bool("json", false, "Als JSON ausgeben")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := ksm.Status(*ksmPath)
	if err != nil {
		return err
	}

	if *asJSON {
		b, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	keys := make([]string, 0, len(st))
	for k := range st {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("KSM Status (%s)\n", *ksmPath)
	for _, k := range keys {
		fmt.Printf("  %-20s %d\n", k, st[k])
	}
	return nil
}

func cmdBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	var (
		profile = fs.String("profile", "P1", "Profil: P1 (identisch), P2 (ähnlich), P3 (worst-case)")
		scale   = fs.String("scale", "", "Skala: z.B. 10..80 oder 10..80..10")
		n       = fs.Int("instances", 0, "Alternativ: fixe Anzahl Instanzen")
		memMiB  = fs.Int("mem-mib", 256, "RAM pro Instanz (MiB)")
		warmup  = fs.Int("warmup-sec", 20, "Warmup in Sekunden (Zeit für KSM-Merge)")
		outDir  = fs.String("out", "results", "Output-Verzeichnis")
		publish = fs.String("publish", "", "Optional: zusätzliches JSON an diesen Pfad schreiben (für GitHub Pages), z.B. docs/data/benchmarks.latest.json")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	var instances []int
	if *scale != "" {
		instances, err = parseScale(*scale)
		if err != nil {
			return err
		}
	} else if *n > 0 {
		instances = []int{*n}
	} else {
		return fmt.Errorf("bitte --scale oder --instances angeben")
	}

	ctx := context.Background()
	cfg := bench.Config{
		ExecPath:  exe,
		OutDir:    *outDir,
		KSMPath:   ksm.DefaultPath,
		Profile:   bench.Profile(strings.ToUpper(*profile)),
		Instances: instances,
		MemMiB:    *memMiB,
		Warmup:    time.Duration(*warmup) * time.Second,
	}

	res, err := bench.Run(ctx, cfg)
	if err != nil {
		return err
	}

	fmt.Printf("OK: Benchmark fertig. Report: %s\n", filepath.Join(*outDir, "report.md"))

	if *publish != "" {
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.MkdirAll(filepath.Dir(*publish), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(*publish, b, 0o644); err != nil {
			return err
		}
		fmt.Printf("OK: Published JSON: %s\n", *publish)
	}
	return nil
}

// parseScale parses "min..max" or "min..max..step".
func parseScale(s string) ([]int, error) {
	parts := strings.Split(s, "..")
	if len(parts) != 2 && len(parts) != 3 {
		return nil, fmt.Errorf("ungültiges scale-Format: %q (erwartet min..max oder min..max..step)", s)
	}
	min, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return nil, err
	}
	max, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return nil, err
	}
	step := 1
	if len(parts) == 3 {
		step, err = strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil {
			return nil, err
		}
		if step <= 0 {
			return nil, fmt.Errorf("step muss > 0 sein")
		}
	}
	if min <= 0 || max <= 0 || max < min {
		return nil, fmt.Errorf("ungültige Grenzen: %d..%d", min, max)
	}

	var out []int
	for i := min; i <= max; i += step {
		out = append(out, i)
	}
	return out, nil
}

// cmdHog ist ein kontrollierter RAM-Allocator für Benchmarks.
// Er erzeugt (auf Wunsch) identische Pages zwischen Prozessen (gut für P1/P2) und kann Pages gezielt \"verschmutzen\" (P2/P3).
func cmdHog(args []string) error {
	fs := flag.NewFlagSet("__hog", flag.ContinueOnError)
	var (
		memMiB    = fs.Int("mem-mib", 256, "Allokation (MiB)")
		id        = fs.Int("id", 0, "Instanz-ID")
		dirtyPct  = fs.Float64("dirty-pct", 0, "Prozent der Pages, die pro Instanz individuell gemacht werden (0..100)")
		redirtyMs = fs.Int("redirty-ms", 0, "Wenn >0: alle X ms werden die individuellen Pages erneut beschrieben (verhindert Merge)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *memMiB <= 0 {
		return fmt.Errorf("mem-mib muss > 0 sein")
	}
	if *dirtyPct < 0 || *dirtyPct > 100 {
		return fmt.Errorf("dirty-pct muss 0..100 sein")
	}

	size := int64(*memMiB) * 1024 * 1024
	if size > math.MaxInt32 { // keep it reasonable for MVP
		return fmt.Errorf("mem-mib ist zu groß für dieses MVP")
	}

	pageSize := os.Getpagesize()
	buf := make([]byte, int(size))

	// Page-Template: identisch über alle Prozesse (damit KSM wirklich mergen kann)
	template := make([]byte, pageSize)
	for i := 0; i < len(template); i += 8 {
		binary.LittleEndian.PutUint64(template[i:], 0x44454E5331545930) // "DENS1TY0" als konstant
	}
	for off := 0; off+pageSize <= len(buf); off += pageSize {
		copy(buf[off:off+pageSize], template)
	}

	// Welche Pages machen wir individuell?
	totalPages := len(buf) / pageSize
	dirtyPages := int(float64(totalPages) * (*dirtyPct / 100.0))
	indices := make([]int, 0, dirtyPages)
	if dirtyPages > 0 {
		for j := 0; j < dirtyPages; j++ {
			// deterministisch, aber pro Instanz unterschiedlich:
			idx := int((uint64(*id)*1315423911 + uint64(j)*2654435761) % uint64(totalPages))
			indices = append(indices, idx)
		}
		applyDirty(buf, pageSize, *id, indices, 0)
	}

	// Signal handling: sauber beenden.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var ticker *time.Ticker
	if *redirtyMs > 0 && len(indices) > 0 {
		ticker = time.NewTicker(time.Duration(*redirtyMs) * time.Millisecond)
		defer ticker.Stop()
	}

	var counter uint64
	if ticker == nil {
		// Kein redirty: einfach warten, bis wir beendet werden.
		<-sigCh
		return nil
	}

	for {
		select {
		case <-sigCh:
			return nil
		case <-ticker.C:
			counter++
			applyDirty(buf, pageSize, *id, indices, counter)
		}
	}
}

func applyDirty(buf []byte, pageSize int, id int, indices []int, counter uint64) {
	for _, idx := range indices {
		off := idx * pageSize
		if off+8 <= len(buf) {
			// Write unique marker at beginning of the page
			v := (uint64(id) << 32) ^ counter ^ 0xBADC0FFEE
			binary.LittleEndian.PutUint64(buf[off:], v)
		}
	}
}
