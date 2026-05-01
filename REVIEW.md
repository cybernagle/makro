# PR #2: Add tmux server selection and chat autocomplete

## Round 1 — 6 comments
| # | File | Issue | Status | Commit |
|---|------|-------|--------|--------|
| 1 | chat.go:224 | selectedSugg out of range — clamp before indexing | ✅ fixed | d2d23f4 |
| 2 | tui_test.go:407 | Tab test has no real assertion — doesn't simulate Tab | ✅ fixed | d2d23f4 |
| 3 | tui_test.go:428 | Leftover unused scaffolding (capturedText, origSendFn) | ✅ fixed | d2d23f4 |
| 4 | main.go:173 | ReadString error ignored in interactive prompt | ✅ fixed | d2d23f4 |
| 5 | client.go:107 | Channel close race — Stop() closes notifs while goroutines active | ✅ fixed | d2d23f4 |
| 6 | styles.go:63 | Inconsistent indentation — needs gofmt | ✅ fixed | d2d23f4 |

## Round 2 — 5 comments
| # | File | Issue | Status | Commit |
|---|------|-------|--------|--------|
| 7 | detect_test.go:38 | TestDetectServerNone missing nil assertion | ✅ fixed | d00e750 |
| 8 | chat.go:401 | Status bar line not reserved in layout calculation | ✅ fixed | d00e750 |
| 9 | chat.go:208 | Enter triggers autocomplete — blocks message submit | ✅ fixed | d00e750 |
| 10 | app.go:104 | @auth without space doesn't switch viewer | ✅ fixed | d00e750 |
| 11 | detect.go:56 | os.Getuid() fails on Windows — add build tags | ✅ fixed | d00e750 |

## Round 3 — 1 comment
| # | File | Issue | Status | Commit |
|---|------|-------|--------|--------|
| 12 | detect.go:10 | Duplicate symbol: detect.go vs detect_unix.go | ✅ N/A — detect.go already renamed, no duplicate | — |

## Round 4 — 4 comments
| # | File | Issue | Status | Commit |
|---|------|-------|--------|--------|
| 13 | detect.go:75 | ServerInfo/DetectServer defined in multiple files | ✅ fixed — moved ServerInfo to detect_types.go | TBD |
| 14 | app.go:52 | CommandRegistry.List() nondeterministic order | ✅ fixed — sort cmds by Name | TBD |
| 15 | styles.go:63 | Indentation not gofmt-aligned | ✅ fixed — ran gofmt | TBD |
| 16 | chat.go:165 | extractMention duplicates agent.ExtractMention | ✅ fixed — removed local copy, use agent.ExtractMention | b967bfd |

## Round 5 — 3 comments
| # | File | Issue | Status | Commit |
|---|------|-------|--------|--------|
| 17 | main.go:176 | ReadString error ignored (re-raise on shifted line) | ✅ fixed — handle error, default to detected server | TBD |
| 18 | chat.go:235 | Double @prefix when user explicitly types @mention with sticky target | ✅ fixed — only prepend if input doesn't start with @ | TBD |
| 19 | detect_test.go:28 | Test file calls unix-only functions, won't compile on Windows | ✅ fixed — added //go:build !windows tag | TBD |
