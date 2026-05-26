package metrics

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// BuildRecord holds metrics for a single build
type BuildRecord struct {
	DrvPath  string
	PName    string // from derivation env
	Version  string // from derivation env
	System   string // e.g. "aarch64-linux"
	Builder  string // builder VM ID

	Status   uint64 // BuildResultStatus
	ErrorMsg string

	StartTime time.Time
	StopTime  time.Time

	// CPU time reported by nix-daemon (microseconds)
	CpuUserUsec   *uint64
	CpuSystemUsec *uint64

	// Peak memory usage in bytes (from builder VM cgroup)
	PeakMemoryBytes *uint64
}

// Store persists build metrics in SQLite
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a SQLite metrics database at the given path
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening metrics db: %w", err)
	}

	// WAL mode for concurrent reads
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting WAL mode: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating metrics db: %w", err)
	}

	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS builds (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			drv_path      TEXT NOT NULL,
			pname         TEXT NOT NULL DEFAULT '',
			version       TEXT NOT NULL DEFAULT '',
			system        TEXT NOT NULL DEFAULT '',
			builder_id    TEXT NOT NULL DEFAULT '',

			status        INTEGER NOT NULL,
			error_msg     TEXT NOT NULL DEFAULT '',

			start_time    INTEGER NOT NULL DEFAULT 0,  -- unix seconds
			stop_time     INTEGER NOT NULL DEFAULT 0,  -- unix seconds

			cpu_user_usec     INTEGER,  -- NULL if not reported
			cpu_system_usec   INTEGER,  -- NULL if not reported
			peak_memory_bytes INTEGER,  -- NULL if not reported

			recorded_at   INTEGER NOT NULL DEFAULT (unixepoch())
		)
	`)
	if err != nil {
		return err
	}

	// Index for lookups by pname (the primary query key for resource estimation)
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_builds_pname ON builds(pname)`)
	if err != nil {
		return err
	}

	// Migration: add peak_memory_bytes column if missing (for existing databases)
	db.Exec(`ALTER TABLE builds ADD COLUMN peak_memory_bytes INTEGER`)

	return nil
}

// Record inserts a build record
func (s *Store) Record(rec BuildRecord) {
	startUnix := int64(0)
	if !rec.StartTime.IsZero() {
		startUnix = rec.StartTime.Unix()
	}
	stopUnix := int64(0)
	if !rec.StopTime.IsZero() {
		stopUnix = rec.StopTime.Unix()
	}

	_, err := s.db.Exec(`
		INSERT INTO builds (drv_path, pname, version, system, builder_id, status, error_msg, start_time, stop_time, cpu_user_usec, cpu_system_usec, peak_memory_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.DrvPath,
		rec.PName,
		rec.Version,
		rec.System,
		rec.Builder,
		rec.Status,
		rec.ErrorMsg,
		startUnix,
		stopUnix,
		rec.CpuUserUsec,
		rec.CpuSystemUsec,
		rec.PeakMemoryBytes,
	)
	if err != nil {
		log.Printf("metrics: failed to record build: %v", err)
	}
}

// BuildStats holds aggregated statistics for a package
type BuildStats struct {
	PName         string
	BuildCount    int
	SuccessCount  int
	AvgDurationS  float64
	P90DurationS  float64
	AvgCpuUserS   *float64
	AvgCpuSystemS *float64
}

// StatsForPackage returns aggregated build stats for a pname
func (s *Store) StatsForPackage(pname string) (*BuildStats, error) {
	stats := &BuildStats{PName: pname}

	err := s.db.QueryRow(`
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status IN (0, 1, 2)),
			COALESCE(AVG(stop_time - start_time) FILTER (WHERE status IN (0, 1, 2) AND stop_time > start_time), 0),
			COALESCE(AVG(cpu_user_usec) FILTER (WHERE status IN (0, 1, 2) AND cpu_user_usec IS NOT NULL), 0) / 1e6,
			COALESCE(AVG(cpu_system_usec) FILTER (WHERE status IN (0, 1, 2) AND cpu_system_usec IS NOT NULL), 0) / 1e6
		FROM builds WHERE pname = ?
	`, pname).Scan(
		&stats.BuildCount,
		&stats.SuccessCount,
		&stats.AvgDurationS,
		&stats.AvgCpuUserS,
		&stats.AvgCpuSystemS,
	)
	if err != nil {
		return nil, err
	}

	// p90 duration from successful builds
	err = s.db.QueryRow(`
		SELECT COALESCE(stop_time - start_time, 0) as duration
		FROM builds
		WHERE pname = ? AND status IN (0, 1, 2) AND stop_time > start_time
		ORDER BY duration ASC
		LIMIT 1 OFFSET (
			SELECT CAST(COUNT(*) * 0.9 AS INTEGER)
			FROM builds
			WHERE pname = ? AND status IN (0, 1, 2) AND stop_time > start_time
		)
	`, pname, pname).Scan(&stats.P90DurationS)
	if err == sql.ErrNoRows {
		stats.P90DurationS = 0
	} else if err != nil {
		return nil, err
	}

	return stats, nil
}

// RecordBuild implements nixproto.MetricsRecorder
func (s *Store) RecordBuild(drvPath string, env map[string]string, platform string, builderID string, status uint64, errorMsg string, startTime uint64, stopTime uint64, cpuUserUsec *uint64, cpuSystemUsec *uint64, peakMemoryBytes *uint64) {
	pname := ExtractPName(env, drvPath)
	version := env["version"]

	var start, stop time.Time
	if startTime > 0 {
		start = time.Unix(int64(startTime), 0)
	}
	if stopTime > 0 {
		stop = time.Unix(int64(stopTime), 0)
	}

	s.Record(BuildRecord{
		DrvPath:         drvPath,
		PName:           pname,
		Version:         version,
		System:          platform,
		Builder:         builderID,
		Status:          status,
		ErrorMsg:        errorMsg,
		StartTime:       start,
		StopTime:        stop,
		CpuUserUsec:     cpuUserUsec,
		CpuSystemUsec:   cpuSystemUsec,
		PeakMemoryBytes: peakMemoryBytes,
	})

	dur := ""
	if stopTime > startTime {
		dur = fmt.Sprintf(" (%ds)", stopTime-startTime)
	}
	cpuInfo := ""
	if cpuUserUsec != nil || cpuSystemUsec != nil {
		u, sy := float64(0), float64(0)
		if cpuUserUsec != nil {
			u = float64(*cpuUserUsec) / 1e6
		}
		if cpuSystemUsec != nil {
			sy = float64(*cpuSystemUsec) / 1e6
		}
		cpuInfo = fmt.Sprintf(" cpu=%.1fu+%.1fs", u, sy)
	}
	memInfo := ""
	if peakMemoryBytes != nil {
		memMB := float64(*peakMemoryBytes) / (1024 * 1024)
		memInfo = fmt.Sprintf(" mem=%.0fMB", memMB)
	}
	log.Printf("metrics: recorded build %s (pname=%s)%s%s%s status=%d", drvPath, pname, dur, cpuInfo, memInfo, status)
}

// Close closes the database
func (s *Store) Close() error {
	return s.db.Close()
}

// ExtractPName extracts pname from a derivation's env map
// Falls back to parsing the drv path basename
func ExtractPName(env map[string]string, drvPath string) string {
	if pname, ok := env["pname"]; ok && pname != "" {
		return pname
	}
	// Fallback: parse from drv path like /nix/store/hash-pname-version.drv
	base := drvPath
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	// Remove .drv suffix
	base = strings.TrimSuffix(base, ".drv")
	// Remove the hash- prefix (32 chars + dash)
	if len(base) > 33 && base[32] == '-' {
		base = base[33:]
	}
	// Remove version suffix (everything after last dash that starts with a digit)
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '-' && i+1 < len(base) && base[i+1] >= '0' && base[i+1] <= '9' {
			return base[:i]
		}
	}
	return base
}
