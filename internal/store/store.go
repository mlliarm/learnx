// Package store persists syllabi, generated lessons, exercise attempts and
// the review schedule in a SQLite database.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db  *sql.DB
	dir string
}

const schema = `
CREATE TABLE IF NOT EXISTS docs (
	pack TEXT NOT NULL,
	key  TEXT NOT NULL,
	data TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (pack, key)
);
CREATE TABLE IF NOT EXISTS attempts (
	id       INTEGER PRIMARY KEY,
	pack     TEXT NOT NULL,
	lesson   TEXT NOT NULL,
	exercise INTEGER NOT NULL,
	correct  INTEGER NOT NULL,
	answer   TEXT NOT NULL,
	at       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS attempts_lesson ON attempts (pack, lesson);
CREATE TABLE IF NOT EXISTS reviews (
	pack     TEXT NOT NULL,
	lesson   TEXT NOT NULL,
	exercise INTEGER NOT NULL,
	box      INTEGER NOT NULL,
	due      INTEGER NOT NULL,
	PRIMARY KEY (pack, lesson, exercise)
);
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`

// Open uses $LEARNX_HOME, else $XDG_DATA_HOME/learnx, else ~/.local/share/learnx.
// JSON files left there by earlier versions are imported once.
func Open() (*Store, error) {
	dir := os.Getenv("LEARNX_HOME")
	if dir == "" {
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			base = filepath.Join(home, ".local", "share")
		}
		dir = filepath.Join(base, "learnx")
	}
	return OpenDir(dir)
}

func OpenDir(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dir, "learnx.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, dir: dir}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init database: %w", err)
	}
	if err := s.importJSON(); err != nil {
		db.Close()
		return nil, fmt.Errorf("import JSON data: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Dir() string { return s.dir }

// Save stores v as JSON under (pack, key), replacing what was there.
func (s *Store) Save(pack, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO docs (pack, key, data, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (pack, key) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		pack, key, string(data), time.Now().Unix())
	return err
}

// Load reports false when nothing has been saved under that key yet.
func (s *Store) Load(pack, key string, v any) (bool, error) {
	var data string
	err := s.db.QueryRow(`SELECT data FROM docs WHERE pack = ? AND key = ?`, pack, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(data), v); err != nil {
		return false, fmt.Errorf("corrupt %s/%s: %w", pack, key, err)
	}
	return true, nil
}

func (s *Store) Exists(pack, key string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM docs WHERE pack = ? AND key = ?`, pack, key).Scan(&one) == nil
}

// Attempt is one answer to one exercise, identified by its index in the lesson.
type Attempt struct {
	Pack     string
	Lesson   string
	Exercise int
	Correct  bool
	Answer   string
	At       time.Time
}

// Intervals are the waits after a correct review, by Leitner box. An exercise
// answered correctly in the last box leaves the review queue.
var Intervals = []time.Duration{24 * time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour, 16 * 24 * time.Hour, 35 * 24 * time.Hour}

// RecordAttempt logs an attempt and updates the review schedule: a miss puts
// the exercise in box 0, due at once; a correct answer to a queued exercise
// moves it up a box and pushes its due date out.
func (s *Store) RecordAttempt(a Attempt) error {
	if a.At.IsZero() {
		a.At = time.Now()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO attempts (pack, lesson, exercise, correct, answer, at) VALUES (?, ?, ?, ?, ?, ?)`,
		a.Pack, a.Lesson, a.Exercise, a.Correct, a.Answer, a.At.Unix()); err != nil {
		return err
	}
	if !a.Correct {
		_, err = tx.Exec(`INSERT INTO reviews (pack, lesson, exercise, box, due) VALUES (?, ?, ?, 0, ?)
			ON CONFLICT (pack, lesson, exercise) DO UPDATE SET box = 0, due = excluded.due`,
			a.Pack, a.Lesson, a.Exercise, a.At.Unix())
	} else {
		var box int
		err = tx.QueryRow(`SELECT box FROM reviews WHERE pack = ? AND lesson = ? AND exercise = ?`,
			a.Pack, a.Lesson, a.Exercise).Scan(&box)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			err = nil // not queued; nothing to schedule
		case err != nil:
		case box >= len(Intervals)-1:
			_, err = tx.Exec(`DELETE FROM reviews WHERE pack = ? AND lesson = ? AND exercise = ?`,
				a.Pack, a.Lesson, a.Exercise)
		default:
			_, err = tx.Exec(`UPDATE reviews SET box = ?, due = ? WHERE pack = ? AND lesson = ? AND exercise = ?`,
				box+1, a.At.Add(Intervals[box]).Unix(), a.Pack, a.Lesson, a.Exercise)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ExerciseStat summarizes the attempts at one exercise.
type ExerciseStat struct {
	Attempts int
	Correct  bool // answered correctly at least once
}

// LessonStats returns stats keyed by exercise index; unattempted ones are absent.
func (s *Store) LessonStats(pack, lesson string) (map[int]ExerciseStat, error) {
	rows, err := s.db.Query(`SELECT exercise, count(*), max(correct) FROM attempts
		WHERE pack = ? AND lesson = ? GROUP BY exercise`, pack, lesson)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := map[int]ExerciseStat{}
	for rows.Next() {
		var ex, n int
		var correct bool
		if err := rows.Scan(&ex, &n, &correct); err != nil {
			return nil, err
		}
		stats[ex] = ExerciseStat{Attempts: n, Correct: correct}
	}
	return stats, rows.Err()
}

// ResetLesson forgets attempts and reviews of a lesson, e.g. after it is rewritten.
func (s *Store) ResetLesson(pack, lesson string) error {
	for _, table := range []string{"attempts", "reviews"} {
		if _, err := s.db.Exec(`DELETE FROM `+table+` WHERE pack = ? AND lesson = ?`, pack, lesson); err != nil {
			return err
		}
	}
	return nil
}

// Review is an exercise in the review queue.
type Review struct {
	Lesson   string
	Exercise int
	Box      int
	Due      time.Time
}

// DueReviews lists queued exercises due by now, oldest first; all queued ones
// if now is zero.
func (s *Store) DueReviews(pack string, now time.Time) ([]Review, error) {
	due := int64(1<<63 - 1)
	if !now.IsZero() {
		due = now.Unix()
	}
	rows, err := s.db.Query(`SELECT lesson, exercise, box, due FROM reviews
		WHERE pack = ? AND due <= ? ORDER BY due, lesson, exercise`, pack, due)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var r Review
		var at int64
		if err := rows.Scan(&r.Lesson, &r.Exercise, &r.Box, &at); err != nil {
			return nil, err
		}
		r.Due = time.Unix(at, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// importJSON loads <dir>/<pack>/{syllabus,lessons/*,progress/*}.json written
// by the JSON store, once. The files are left in place as a backup.
func (s *Store) importJSON() error {
	var done string
	if s.db.QueryRow(`SELECT value FROM meta WHERE key = 'json_imported'`).Scan(&done) == nil {
		return nil
	}
	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		rel, _ := filepath.Rel(s.dir, path)
		pack, key, ok := strings.Cut(strings.TrimSuffix(filepath.ToSlash(rel), ".json"), "/")
		if !ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if lesson, ok := strings.CutPrefix(key, "progress/"); ok {
			return s.importProgress(pack, lesson, data)
		}
		var v json.RawMessage
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return s.Save(pack, key, v)
	})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO meta (key, value) VALUES ('json_imported', '1')`)
	return err
}

// importProgress turns the JSON store's per-exercise counts into attempts.
func (s *Store) importProgress(pack, lesson string, data []byte) error {
	var p struct {
		Exercises []struct {
			Attempts int  `json:"attempts"`
			Correct  bool `json:"correct"`
		} `json:"exercises"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	for i, e := range p.Exercises {
		for n := 1; n <= e.Attempts; n++ {
			if err := s.RecordAttempt(Attempt{Pack: pack, Lesson: lesson, Exercise: i,
				Correct: e.Correct && n == e.Attempts}); err != nil {
				return err
			}
		}
	}
	return nil
}
