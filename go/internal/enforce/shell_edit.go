package enforce

import (
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var (
	shellAssignment     = regexp.MustCompile(`(?m)\b([A-Za-z_]\w*)\s*=\s*(['"])([^'"\r\n]*)['"]`)
	shellPathAssignment = regexp.MustCompile(`(?is)\b([A-Za-z_]\w*)\s*=\s*Path\s*\(\s*(['"])([^'"]+)['"]\s*\)`)
	shellOpen           = regexp.MustCompile(`(?is)\bopen\s*\(\s*([^,()]+),([^)]*)\)`)
	shellWriteMode      = regexp.MustCompile(`(?is)(?:\bmode\s*=\s*(['"])([^'"]*)['"]|^\s*(['"])([^'"]*)['"])`)
	shellPathWrite      = regexp.MustCompile(`(?is)\bPath\s*\(\s*([^()]+)\s*\)\s*\.\s*write_(?:text|bytes)\s*\(`)
	shellFSWrite        = regexp.MustCompile(`(?is)\b(?:fs\.)?(?:writeFileSync|writeFile|appendFileSync|appendFile)\s*\(\s*([^,()]+)`)
	shellVarWrite       = regexp.MustCompile(`(?is)\b([A-Za-z_]\w*)\s*\.\s*write_(?:text|bytes)\s*\(`)
	shellIORight        = regexp.MustCompile(`(?is)\[\s*(?:System\.)?IO\.File\s*\]\s*::\s*(?:Write\w*|Append\w*)\s*\(\s*(['"])([^'"]+)['"]`)
)

// shellEditTargets returns literal source paths that recognized shell write forms can modify.
func shellEditTargets(tool, command string) (targets []string, trivial bool) {
	if tool != "Bash" && tool != "PowerShell" {
		return nil, false
	}
	segments, scripts := shellSegments(command)
	cwd := ""
	cwdKnown := true
	seen := map[string]bool{}
	add := func(path string) {
		path = shellPath(path)
		if path == "" || !editSourcePath(path) || seen[path] {
			return
		}
		seen[path] = true
		targets = append(targets, path)
	}
	addAtCwd := func(path string) {
		if !filepath.IsAbs(shellPath(path)) && !cwdKnown {
			return
		}
		add(shellJoin(cwd, path))
	}
	trivialCount := 0
	var trivialFiles []string
	for _, segment := range segments {
		words := shellWords(segment)
		if len(words) == 0 {
			continue
		}
		if strings.HasPrefix(words[0], "#") {
			continue
		}
		cmd := strings.ToLower(words[0])
		if strings.HasPrefix(strings.TrimSpace(segment), "(") && strings.Contains(strings.ToLower(segment), "cd ") {
			cwdKnown = false
			continue
		}
		if cmd == "cd" || cmd == "pushd" || cmd == "set-location" || cmd == "sl" {
			if len(words) > 1 && words[1] != "-" && shellLiteral(words[1]) {
				path := shellPath(words[1])
				if filepath.IsAbs(path) {
					cwd, cwdKnown = path, true
				} else if cwdKnown {
					cwd = shellJoin(cwd, path)
				} else {
					cwdKnown = false
				}
			} else {
				cwdKnown = false
			}
			continue
		}
		if strings.EqualFold(words[0], "git") {
			continue
		}
		lower := strings.ToLower(words[0])
		if tool == "Bash" && (lower == "sed" || lower == "perl") {
			inplace := false
			for i := 1; i < len(words) && strings.HasPrefix(words[i], "-"); i++ {
				o := words[i]
				if lower == "sed" && (o == "--in-place" || strings.HasPrefix(o, "--in-place=") || strings.HasPrefix(o, "-") && !strings.HasPrefix(o, "--") && strings.Contains(o[1:], "i")) {
					inplace = true
				}
				if lower == "perl" && regexp.MustCompile(`^-[A-Za-z]*i(?:\..*)?$`).MatchString(o) {
					inplace = true
				}
			}
			if inplace {
				script := ""
				for _, word := range words[1:] {
					if !strings.HasPrefix(word, "-") {
						script = word
						break
					}
				}
				files := shellFileOperands(words, script)
				if lower == "sed" && len(files) == 1 && regexp.MustCompile(`^\d+s/[^/]+/[^/]*(?:/[gIp]*)?$`).MatchString(script) {
					trivialCount++
					if cwdKnown || filepath.IsAbs(shellPath(files[0])) {
						trivialFiles = append(trivialFiles, shellJoin(cwd, files[0]))
					}
				} else {
					for _, f := range files {
						addAtCwd(f)
					}
				}
			}
		}
		if tool == "Bash" && (lower == "python" || lower == "python3" || lower == "py" || lower == "node") {
			script := ""
			for i, w := range words {
				if (w == "-c" || w == "-e") && i+1 < len(words) {
					script = words[i+1]
					break
				}
			}
			if script == "" {
				script = scripts[segment]
			}
			addScriptTargets(script, addAtCwd)
		}
		if tool == "PowerShell" || tool == "Bash" {
			if strings.EqualFold(words[0], "Set-Content") || strings.EqualFold(words[0], "Add-Content") || strings.EqualFold(words[0], "Out-File") {
				path := shellParameter(words, "-Path", "-FilePath", "-LiteralPath")
				if path == "" && len(words) > 1 && !strings.HasPrefix(words[1], "-") {
					path = words[1]
				}
				addAtCwd(path)
			}
			for _, match := range shellIORight.FindAllStringSubmatch(segment, -1) {
				addAtCwd(match[2])
			}
		}
		if tool == "Bash" {
			if lower == "tee" {
				for _, w := range words[1:] {
					if w == "-a" || strings.HasPrefix(w, "-") {
						continue
					}
					addAtCwd(w)
				}
			}
		}
		for _, path := range shellRedirections(segment) {
			addAtCwd(path)
		}
	}
	if trivialCount > 1 {
		for _, path := range trivialFiles {
			add(path)
		}
	}
	return targets, trivialCount == 1 && len(targets) == 0
}

func shellPath(path string) string {
	path = strings.TrimSpace(path)
	if !shellLiteral(path) {
		return ""
	}
	if runtime.GOOS == "windows" && len(path) > 2 && path[0] == '/' && path[2] == '/' && path[1] >= 'a' && path[1] <= 'z' {
		path = strings.ToUpper(path[1:2]) + ":" + path[2:]
	}
	return filepath.Clean(filepath.FromSlash(path))
}

func shellJoin(cwd, path string) string {
	path = shellPath(path)
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(cwd, path)
}

func shellLiteral(path string) bool {
	longPath := strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `//?/`)
	return path != "" && !strings.ContainsAny(path, "$*[]`<>|&\r\n") && (longPath || !strings.Contains(path, "?")) && !strings.Contains(path, "(") && !strings.Contains(path, ")")
}

func shellSegments(command string) ([]string, map[string]string) {
	lines := strings.Split(command, "\n")
	var clean []string
	scripts := map[string]string{}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		clean = append(clean, line)
		if marker := heredocMarker(line); marker != "" {
			var body []string
			for i++; i < len(lines) && strings.TrimSpace(lines[i]) != marker; i++ {
				body = append(body, lines[i])
			}
			scripts[line] = strings.Join(body, "\n")
		}
	}
	parts := splitShell(strings.Join(clean, "\n"))
	for _, part := range parts {
		if !strings.Contains(part, "<<") {
			continue
		}
		for line, body := range scripts {
			if strings.Contains(line, part) && (strings.Contains(strings.ToLower(line), "python") || strings.Contains(strings.ToLower(line), "node") || strings.Contains(strings.ToLower(line), "py ")) {
				scripts[part] = body
				break
			}
		}
	}
	return parts, scripts
}

func heredocMarker(line string) string {
	i := strings.Index(line, "<<")
	if i < 0 || i+2 < len(line) && line[i+2] == '<' {
		return ""
	}
	start := i + 2
	if start < len(line) && line[start] == '-' {
		start++
	}
	rest := strings.TrimSpace(line[start:])
	if rest == "" {
		return ""
	}
	marker := strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == ';' || r == '|' || r == '&' })[0]
	return strings.Trim(marker, "'\"")
}

func splitShell(s string) []string {
	var out []string
	start := 0
	var quote rune
	escaped := false
	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ';' || r == '\n' || r == '|' || r == '&' {
			if i > 0 && s[i-1] == byte(r) && (r == '|' || r == '&') {
				continue
			}
			part := strings.TrimSpace(s[start:i])
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
			if i+1 < len(s) && s[i+1] == byte(r) && (r == '&' || r == '|') {
				start++
			}
		}
	}
	if p := strings.TrimSpace(s[start:]); p != "" {
		out = append(out, p)
	}
	return out
}

func shellWords(s string) []string {
	var out []string
	var b strings.Builder
	var q rune
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for i, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && q != '\'' {
			next := rune(0)
			if i+1 < len(s) {
				next = rune(s[i+1])
			}
			if runtime.GOOS != "windows" || strings.ContainsRune(" \t\r\n'\"\\&|;<>", next) {
				escaped = true
				continue
			}
		}
		if q != 0 {
			if r == q {
				q = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			q = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\r' {
			flush()
		} else {
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

func shellFileOperands(words []string, script string) []string {
	var out []string
	skippedScript := false
	for i := 1; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") {
			continue
		}
		if !skippedScript && w == script {
			skippedScript = true
			continue
		}
		out = append(out, w)
	}
	return out
}

func addScriptTargets(script string, add func(string)) {
	vars := map[string]string{}
	for _, m := range shellAssignment.FindAllStringSubmatch(script, -1) {
		vars[m[1]] = m[3]
	}
	for _, m := range shellPathAssignment.FindAllStringSubmatch(script, -1) {
		vars[m[1]] = m[3]
	}
	for _, m := range shellOpen.FindAllStringSubmatch(script, -1) {
		mode := shellWriteMode.FindStringSubmatch(m[2])
		if mode == nil {
			continue
		}
		modeText := mode[2]
		if modeText == "" {
			modeText = mode[4]
		}
		if !strings.ContainsAny(modeText, "wax+") {
			continue
		}
		path := strings.Trim(strings.TrimSpace(m[1]), "'\"")
		if vars[path] != "" {
			path = vars[path]
		}
		add(path)
	}
	for _, m := range shellPathWrite.FindAllStringSubmatch(script, -1) {
		path := strings.Trim(strings.TrimSpace(m[1]), "'\"")
		if vars[path] != "" {
			path = vars[path]
		}
		add(path)
	}
	for _, m := range shellVarWrite.FindAllStringSubmatch(script, -1) {
		if vars[m[1]] != "" {
			add(vars[m[1]])
		}
	}
	for _, m := range shellFSWrite.FindAllStringSubmatch(script, -1) {
		path := strings.Trim(strings.TrimSpace(m[1]), "'\"")
		if vars[path] != "" {
			path = vars[path]
		}
		add(path)
	}
}

func shellParameter(words []string, names ...string) string {
	for i, w := range words {
		for _, name := range names {
			if strings.EqualFold(w, name) && i+1 < len(words) {
				return words[i+1]
			}
		}
	}
	return ""
}

func shellRedirections(segment string) []string {
	var out []string
	trimmed := strings.TrimSpace(segment)
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "test ") {
		return nil
	}
	var q rune
	escaped := false
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && q != '\'' {
			if runtime.GOOS != "windows" || i+1 < len(segment) && strings.ContainsRune(" \t\r\n'\"\\&|;<>", rune(segment[i+1])) {
				escaped = true
				continue
			}
		}
		if q != 0 {
			if c == byte(q) {
				q = 0
			}
			continue
		}
		if strings.HasPrefix(segment[i:], "$((") {
			if end := strings.Index(segment[i+3:], "))"); end >= 0 {
				i += end + 4
				continue
			}
		}
		if strings.HasPrefix(segment[i:], "[[") {
			if end := strings.Index(segment[i+2:], "]]"); end >= 0 {
				i += end + 3
				continue
			}
		}
		if c == '\'' || c == '"' {
			q = rune(c)
			continue
		}
		if c != '>' || i > 0 && (segment[i-1] == '2' || segment[i-1] == '&') {
			continue
		}
		if i+1 < len(segment) && segment[i+1] == '&' {
			continue
		}
		j := i + 1
		if j < len(segment) && segment[j] == '>' {
			j++
		}
		for j < len(segment) && (segment[j] == ' ' || segment[j] == '\t') {
			j++
		}
		if j < len(segment) && (segment[j] == '\'' || segment[j] == '"') {
			quote := segment[j]
			j++
			start := j
			for j < len(segment) && segment[j] != quote {
				j++
			}
			if start < j {
				out = append(out, segment[start:j])
			}
			for j < len(segment) && segment[j] != ' ' && segment[j] != '\t' {
				j++
			}
			i = j - 1
			continue
		}
		start := j
		for j < len(segment) && !strings.ContainsRune(" \t\r\n;|&", rune(segment[j])) {
			j++
		}
		if start < j {
			p := segment[start:j]
			if p != "/dev/null" && !strings.EqualFold(p, "NUL") && !strings.EqualFold(p, "$null") {
				out = append(out, p)
			}
			i = j - 1
		}
	}
	return out
}
