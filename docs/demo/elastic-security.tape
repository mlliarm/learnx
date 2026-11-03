# Elastic Security: syllabus, a lesson, a hint, and an ES|QL query graded
# against a live Elasticsearch. Record with docs/demo/record.sh.

Output docs/images/elastic-security.gif

Set Shell "bash"
Set FontSize 15
Set Width 1280
Set Height 760
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 25ms

Hide
Type "PS1='$ ' && clear"
Enter
Show

Type "learnx tui elastic-security"
Sleep 500ms
Enter
Sleep 3s

# Lesson 1.2
Down
Sleep 1s
Enter
Sleep 2.5s
PageDown
Sleep 1.5s
PageDown
Sleep 1.5s

# Practice: a multiple-choice question with a hint
Type "p"
Sleep 2.5s
Ctrl+G
Sleep 2.5s
Type "a"
Sleep 3s
Enter
Sleep 2s

# An ES|QL exercise: a hint, a wrong answer, then the right one
Ctrl+G
Sleep 2.5s
Type `FROM auth-events | WHERE event.category == "authentication" | STATS event_count = COUNT(*) BY host.name | SORT host.name`
Sleep 1s
Ctrl+S
Sleep 3s
Ctrl+U
Sleep 500ms
Type `FROM auth-events | WHERE event.category == "authentication" | STATS n = COUNT(*) BY host.name, event.outcome | SORT host.name, event.outcome`
Sleep 1s
Ctrl+S
Sleep 4s

# Back to the syllabus, which now shows progress
Ctrl+B
Sleep 4s
Hide
Type "q"
Sleep 500ms
