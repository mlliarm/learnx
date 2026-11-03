package practice

import (
	"fmt"
	"time"

	"github.com/mlliarm/learnx/internal/store"
	"github.com/mlliarm/learnx/internal/tutor"
)

// Store keys for a pack's syllabus and lessons.
const SyllabusKey = "syllabus"

func LessonKey(id string) string { return "lessons/" + id }

// Item is one exercise to work on.
type Item struct {
	Lesson *tutor.Lesson
	Index  int
}

func (it Item) Exercise() *tutor.Exercise { return &it.Lesson.Exercises[it.Index] }

// LessonItems lists a lesson's exercises not yet answered correctly, or all
// of them when all is set.
func LessonItems(st *store.Store, pack string, l *tutor.Lesson, all bool) ([]Item, error) {
	stats, err := st.LessonStats(pack, l.ID)
	if err != nil {
		return nil, err
	}
	var items []Item
	for i := range l.Exercises {
		if all || !stats[i].Correct {
			items = append(items, Item{Lesson: l, Index: i})
		}
	}
	return items, nil
}

// DueItems lists queued review exercises due by now (all queued ones if now
// is zero). Exercises whose lesson has since lost them are skipped.
func DueItems(st *store.Store, pack string, now time.Time) ([]Item, error) {
	due, err := st.DueReviews(pack, now)
	if err != nil {
		return nil, err
	}
	lessons := map[string]*tutor.Lesson{}
	var items []Item
	for _, r := range due {
		l, ok := lessons[r.Lesson]
		if !ok {
			l = &tutor.Lesson{}
			found, err := st.Load(pack, LessonKey(r.Lesson), l)
			if err != nil {
				return nil, err
			}
			if !found {
				l = nil
			}
			lessons[r.Lesson] = l
		}
		if l == nil || r.Exercise >= len(l.Exercises) {
			continue
		}
		items = append(items, Item{Lesson: l, Index: r.Exercise})
	}
	return items, nil
}

// LessonProgress counts a lesson's exercises answered correctly.
func LessonProgress(st *store.Store, pack string, l *tutor.Lesson) (done, total int, err error) {
	stats, err := st.LessonStats(pack, l.ID)
	if err != nil {
		return 0, 0, err
	}
	for i := range l.Exercises {
		if stats[i].Correct {
			done++
		}
	}
	return done, len(l.Exercises), nil
}

// NextUnfinished is the first written lesson with exercises not yet correct.
func NextUnfinished(st *store.Store, pack string, syl *tutor.Syllabus) (string, error) {
	written := false
	for _, id := range syl.LessonIDs() {
		var l tutor.Lesson
		found, err := st.Load(pack, LessonKey(id), &l)
		if err != nil {
			return "", err
		}
		if !found {
			continue
		}
		written = true
		done, total, err := LessonProgress(st, pack, &l)
		if err != nil {
			return "", err
		}
		if done < total {
			return id, nil
		}
	}
	if !written {
		return "", fmt.Errorf("no lessons written yet; run `learnx lesson %s` first", pack)
	}
	return "", fmt.Errorf("every written lesson is done; write the next one with `learnx lesson %s` or pass a lesson id with --all", pack)
}
