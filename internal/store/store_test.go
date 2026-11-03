package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDocsRoundTrip(t *testing.T) {
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	type doc struct{ Title string }
	var got doc
	if found, err := s.Load("p", "syllabus", &got); found || err != nil {
		t.Fatalf("Load before Save = %v, %v", found, err)
	}
	if err := s.Save("p", "syllabus", doc{"one"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("p", "syllabus", doc{"two"}); err != nil {
		t.Fatal(err)
	}
	if found, err := s.Load("p", "syllabus", &got); !found || err != nil || got.Title != "two" {
		t.Fatalf("Load = %v, %v, %+v", found, err, got)
	}
	if !s.Exists("p", "syllabus") || s.Exists("p", "lessons/1.1") || s.Exists("q", "syllabus") {
		t.Error("Exists is wrong")
	}
}

func TestAttemptsAndReviewSchedule(t *testing.T) {
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rec := func(ex int, correct bool, at time.Time) {
		t.Helper()
		if err := s.RecordAttempt(Attempt{Pack: "p", Lesson: "1.1", Exercise: ex, Correct: correct, At: at}); err != nil {
			t.Fatal(err)
		}
	}

	rec(0, true, t0)  // right first time: never queued
	rec(1, false, t0) // missed: queued, due now
	rec(1, true, t0.Add(time.Minute))

	stats, err := s.LessonStats("p", "1.1")
	if err != nil {
		t.Fatal(err)
	}
	if stats[0] != (ExerciseStat{1, true}) || stats[1] != (ExerciseStat{2, true}) {
		t.Errorf("stats = %+v", stats)
	}

	due, _ := s.DueReviews("p", t0.Add(time.Hour))
	if len(due) != 0 {
		t.Fatalf("exercise 1 should wait a day after a correct review, due = %+v", due)
	}
	queued, _ := s.DueReviews("p", time.Time{})
	if len(queued) != 1 || queued[0].Exercise != 1 || queued[0].Box != 1 || !queued[0].Due.Equal(t0.Add(time.Minute+Intervals[0])) {
		t.Fatalf("queue = %+v", queued)
	}

	// Climb through every box; the last correct answer graduates it.
	at := t0
	for range Intervals {
		at = at.Add(40 * 24 * time.Hour)
		rec(1, true, at)
	}
	if queued, _ := s.DueReviews("p", time.Time{}); len(queued) != 0 {
		t.Errorf("should have graduated: %+v", queued)
	}

	rec(2, false, t0)
	if err := s.ResetLesson("p", "1.1"); err != nil {
		t.Fatal(err)
	}
	stats, _ = s.LessonStats("p", "1.1")
	queued, _ = s.DueReviews("p", time.Time{})
	if len(stats) != 0 || len(queued) != 0 {
		t.Errorf("ResetLesson left stats=%v queue=%v", stats, queued)
	}
}

func TestImportsJSONOnce(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("elastic-security/syllabus.json", `{"title":"Course"}`)
	write("elastic-security/lessons/1.1.json", `{"title":"Lesson"}`)
	write("elastic-security/progress/1.1.json", `{"exercises":[{"attempts":3,"correct":true},{"attempts":1,"correct":false}]}`)

	s, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Title string }
	if found, _ := s.Load("elastic-security", "lessons/1.1", &doc); !found || doc.Title != "Lesson" {
		t.Errorf("lesson not imported: %v %+v", found, doc)
	}
	stats, _ := s.LessonStats("elastic-security", "1.1")
	if stats[0] != (ExerciseStat{3, true}) || stats[1] != (ExerciseStat{1, false}) {
		t.Errorf("progress not imported: %+v", stats)
	}
	// Exercise 1 was never answered right, so it is due now; exercise 0 was
	// missed twice before succeeding, so it is queued for tomorrow.
	if q, _ := s.DueReviews("elastic-security", time.Now()); len(q) != 1 || q[0].Exercise != 1 {
		t.Errorf("due now = %+v", q)
	}
	if q, _ := s.DueReviews("elastic-security", time.Time{}); len(q) != 2 {
		t.Errorf("queued = %+v", q)
	}
	s.Close()

	// A second open must not import again.
	s, err = OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if stats, _ := s.LessonStats("elastic-security", "1.1"); stats[0].Attempts != 3 {
		t.Errorf("imported twice: %+v", stats)
	}
}
