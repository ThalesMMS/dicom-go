// Command doccheck validates repository documentation without network access.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type config struct {
	Module          string           `json:"module"`
	Include         []string         `json:"include"`
	Exemptions      []exemption      `json:"exemptions"`
	CurrentSections []currentSection `json:"current_sections"`
	PathReferences  []pathReference  `json:"path_references"`
	HelpCommands    []helpCommand    `json:"help_commands"`
}

type exemption struct {
	Pattern string   `json:"pattern"`
	Rules   []string `json:"rules"`
	Reason  string   `json:"reason"`
}

type currentSection struct {
	Path          string `json:"path"`
	Heading       string `json:"heading"`
	AllowedIssues []int  `json:"allowed_issues"`
}

type pathReference struct {
	Document string `json:"document"`
	Text     string `json:"text"`
	Target   string `json:"target"`
}

type helpCommand struct {
	Document       string   `json:"document"`
	Argv           []string `json:"argv"`
	Contains       string   `json:"contains"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

type diagnostic struct {
	Path    string
	Line    int
	Rule    string
	Message string
}

type checker struct {
	repositoryRoot string
	moduleRoot     string
	configuration  config
	diagnostics    []diagnostic
	anchorCache    map[string]map[string]bool
	fileCount      int
	linkCount      int
	anchorCount    int
	helpCount      int
}

type fenceState struct {
	marker byte
	length int
	line   int
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

var (
	referencePattern        = regexp.MustCompile(`^\s*\[([^\]]+)\]:\s*(\S+)`)
	referenceUsePattern     = regexp.MustCompile(`!?\[([^\]]+)\]\[([^\]]*)\]`)
	htmlLinkPattern         = regexp.MustCompile(`(?i)(?:href|src)=["']([^"']+)["']`)
	headingPattern          = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	setextPattern           = regexp.MustCompile(`^\s*(=+|-+)\s*$`)
	explicitIDPattern       = regexp.MustCompile(`(?i)<(?:a\s+(?:name|id)|[^>]+\s+id)=["']([^"']+)["']`)
	issuePattern            = regexp.MustCompile(`(?:^|[^[:alnum:]_])#([0-9]+)\b`)
	issueURLPattern         = regexp.MustCompile(`https?://github\.com/[^/\s]+/[^/\s]+/issues/([0-9]+)\b`)
	directivePattern        = regexp.MustCompile(`<!--\s*doccheck:\s*exempt=([a-z,-]+)\s+reason="([^"]+)"\s*-->`)
	inlineCodePattern       = regexp.MustCompile("`+[^`]*`+")
	markdownLinkTextPattern = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	htmlTagPattern          = regexp.MustCompile(`<[^>]+>`)
)

func main() {
	rootFlag := flag.String("root", "..", "repository root")
	configFlag := flag.String("config", ".doccheck.json", "configuration file")
	flag.Parse()

	repositoryRoot, err := filepath.Abs(*rootFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	configuration, err := loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doccheck: config: %v\n", err)
		os.Exit(2)
	}
	validation := &checker{
		repositoryRoot: repositoryRoot,
		moduleRoot:     filepath.Join(repositoryRoot, filepath.FromSlash(configuration.Module)),
		configuration:  configuration,
		anchorCache:    make(map[string]map[string]bool),
	}
	if err := validation.run(); err != nil {
		fmt.Fprintf(os.Stderr, "doccheck: %v\n", err)
		os.Exit(2)
	}
	if len(validation.diagnostics) != 0 {
		sort.Slice(validation.diagnostics, func(i, j int) bool {
			left, right := validation.diagnostics[i], validation.diagnostics[j]
			if left.Path != right.Path {
				return left.Path < right.Path
			}
			if left.Line != right.Line {
				return left.Line < right.Line
			}
			return left.Rule < right.Rule
		})
		for _, problem := range validation.diagnostics {
			fmt.Fprintf(os.Stderr, "%s:%d: doccheck/%s: %s\n", problem.Path, problem.Line, problem.Rule, problem.Message)
		}
		os.Exit(1)
	}
	fmt.Printf("doccheck: %d Markdown files, %d local links, %d anchors, %d help commands: OK\n",
		validation.fileCount, validation.linkCount, validation.anchorCount, validation.helpCount)
}

func loadConfig(name string) (config, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return config{}, err
	}
	var result config
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return config{}, err
	}
	if strings.TrimSpace(result.Module) == "" || len(result.Include) == 0 {
		return config{}, errors.New("module and include are required")
	}
	for index, item := range result.Exemptions {
		if strings.TrimSpace(item.Pattern) == "" || len(item.Rules) == 0 || strings.TrimSpace(item.Reason) == "" {
			return config{}, fmt.Errorf("exemptions[%d] requires pattern, rules, and reason", index)
		}
		for _, rule := range item.Rules {
			if !knownRule(rule) {
				return config{}, fmt.Errorf("exemptions[%d] names unknown rule %q", index, rule)
			}
		}
	}
	for index, item := range result.PathReferences {
		if strings.TrimSpace(item.Document) == "" || strings.TrimSpace(item.Text) == "" || strings.TrimSpace(item.Target) == "" {
			return config{}, fmt.Errorf("path_references[%d] requires document, text, and target", index)
		}
	}
	for index, item := range result.HelpCommands {
		if strings.TrimSpace(item.Document) == "" || len(item.Argv) != 4 || item.Argv[0] != "go" ||
			item.Argv[1] != "run" || !strings.HasPrefix(item.Argv[2], "./cmd/") || item.Argv[3] != "-h" ||
			item.TimeoutSeconds < 1 || item.TimeoutSeconds > 30 || strings.TrimSpace(item.Contains) == "" {
			return config{}, fmt.Errorf("help_commands[%d] requires a document, exact [go run ./cmd/name -h] argv, contains text, and 1-30 second timeout", index)
		}
		for _, argument := range item.Argv {
			if strings.TrimSpace(argument) == "" {
				return config{}, fmt.Errorf("help_commands[%d] contains an empty argv entry", index)
			}
		}
	}
	return result, nil
}

func knownRule(rule string) bool {
	switch rule {
	case "links", "anchors", "issues", "commands":
		return true
	default:
		return false
	}
}

func (c *checker) run() error {
	if !within(c.repositoryRoot, c.moduleRoot) {
		return errors.New("configured module escapes repository")
	}
	if info, err := os.Stat(c.moduleRoot); err != nil || !info.IsDir() {
		return fmt.Errorf("configured module is not a directory: %s", c.moduleRoot)
	}
	files, err := c.markdownFiles()
	if err != nil {
		return err
	}
	for _, name := range files {
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		c.fileCount++
		c.checkLinks(name, string(content))
	}
	for _, section := range c.configuration.CurrentSections {
		c.checkCurrentSection(section)
	}
	for _, reference := range c.configuration.PathReferences {
		c.checkPathReference(reference)
	}
	for _, command := range c.configuration.HelpCommands {
		c.checkHelpCommand(command)
	}
	return nil
}

func (c *checker) markdownFiles() ([]string, error) {
	seen := make(map[string]bool)
	var result []string
	for _, include := range c.configuration.Include {
		name := filepath.Join(c.moduleRoot, filepath.FromSlash(include))
		if !within(c.moduleRoot, name) {
			return nil, fmt.Errorf("include escapes module: %s", include)
		}
		info, err := os.Stat(name)
		if err != nil {
			return nil, fmt.Errorf("include %s: %w", include, err)
		}
		if !info.IsDir() {
			if strings.EqualFold(filepath.Ext(name), ".md") && !seen[name] {
				seen[name] = true
				result = append(result, name)
			}
			continue
		}
		err = filepath.WalkDir(name, func(candidate string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(candidate), ".md") && !seen[candidate] {
				seen[candidate] = true
				result = append(result, candidate)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(result)
	return result, nil
}

func (c *checker) checkLinks(name, content string) {
	relative := c.moduleRelative(name)
	directives := parseDirectives(content)
	lines := strings.Split(content, "\n")
	definitions := make(map[string]bool)
	var fence fenceState
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if updateFence(trimmed, &fence, index+1) {
			continue
		}
		if fence.length != 0 {
			continue
		}
		plain := stripInlineCode(line)
		if match := referencePattern.FindStringSubmatch(plain); match != nil {
			definitions[normalizeReference(match[1])] = true
		}
	}
	if fence.length != 0 && !c.exempt(relative, "links", directives) {
		c.add(name, fence.line, "links", "unclosed fenced code block")
	}

	fence = fenceState{}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if updateFence(trimmed, &fence, index+1) {
			continue
		}
		if fence.length != 0 {
			continue
		}
		plain := stripInlineCode(line)
		var targets []string
		targets = append(targets, inlineMarkdownTargets(plain)...)
		if match := referencePattern.FindStringSubmatch(plain); match != nil {
			targets = append(targets, markdownDestination(match[2]))
		}
		for _, match := range referenceUsePattern.FindAllStringSubmatch(plain, -1) {
			label := match[2]
			if label == "" {
				label = match[1]
			}
			if !definitions[normalizeReference(label)] {
				if !c.exempt(relative, "links", directives) {
					c.add(name, index+1, "links", fmt.Sprintf("undefined link reference %q", label))
				}
				continue
			}
		}
		for _, match := range htmlLinkPattern.FindAllStringSubmatch(plain, -1) {
			targets = append(targets, strings.TrimSpace(match[1]))
		}
		for _, target := range targets {
			c.checkLinkTarget(name, index+1, target, directives)
		}
	}
}

func updateFence(line string, state *fenceState, lineNumber int) bool {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return false
	}
	marker := line[0]
	length := 1
	for length < len(line) && line[length] == marker {
		length++
	}
	if length < 3 {
		return false
	}
	if state.length == 0 {
		state.marker, state.length, state.line = marker, length, lineNumber
		return true
	}
	if marker == state.marker && length >= state.length && strings.TrimSpace(line[length:]) == "" {
		*state = fenceState{}
		return true
	}
	return false
}

func stripInlineCode(line string) string {
	return inlineCodePattern.ReplaceAllString(line, "")
}

func normalizeReference(label string) string {
	return strings.Join(strings.Fields(strings.ToLower(label)), " ")
}

func inlineMarkdownTargets(line string) []string {
	var targets []string
	for offset := 0; offset < len(line); {
		closeLabel := strings.Index(line[offset:], "](")
		if closeLabel < 0 {
			break
		}
		closeLabel += offset
		if strings.LastIndex(line[:closeLabel], "[") < 0 {
			offset = closeLabel + 2
			continue
		}
		start := closeLabel + 2
		depth, escaped, end := 1, false, -1
		for index := start; index < len(line); index++ {
			character := line[index]
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			switch character {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = index
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			break
		}
		targets = append(targets, markdownDestination(line[start:end]))
		offset = end + 1
	}
	return targets
}

func markdownDestination(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<") {
		if end := strings.Index(value, ">"); end >= 0 {
			return value[1:end]
		}
	}
	if index := strings.IndexAny(value, " \t"); index >= 0 {
		value = value[:index]
	}
	return strings.Trim(value, "<>")
}

func (c *checker) checkLinkTarget(source string, line int, target string, directives map[string]bool) {
	if target == "" || strings.HasPrefix(target, "//") {
		return
	}
	linksExempt := c.exempt(c.moduleRelative(source), "links", directives)
	anchorsExempt := c.exempt(c.moduleRelative(source), "anchors", directives)
	parsed, err := url.Parse(target)
	if err != nil {
		if !linksExempt {
			c.add(source, line, "links", fmt.Sprintf("invalid link target %q: %v", target, err))
		}
		return
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		return
	}
	pathPart, err := url.PathUnescape(parsed.Path)
	if err != nil {
		if !linksExempt {
			c.add(source, line, "links", fmt.Sprintf("invalid escaped path %q: %v", target, err))
		}
		return
	}
	fragment, err := url.PathUnescape(parsed.Fragment)
	if err != nil {
		if !anchorsExempt {
			c.add(source, line, "anchors", fmt.Sprintf("invalid escaped anchor %q: %v", target, err))
		}
		return
	}
	destination := source
	if pathPart != "" {
		if strings.HasPrefix(pathPart, "/") {
			destination = filepath.Join(c.repositoryRoot, filepath.FromSlash(strings.TrimPrefix(pathPart, "/")))
		} else {
			destination = filepath.Join(filepath.Dir(source), filepath.FromSlash(pathPart))
		}
	}
	destination = filepath.Clean(destination)
	if !within(c.repositoryRoot, destination) {
		if !linksExempt {
			c.add(source, line, "links", fmt.Sprintf("local link escapes repository: %q", target))
		}
		return
	}
	if _, err := os.Stat(destination); err != nil {
		if !linksExempt {
			c.add(source, line, "links", fmt.Sprintf("missing local target %q", c.repositoryRelative(destination)))
		}
		return
	}
	if !withinResolved(c.repositoryRoot, destination) {
		if !linksExempt {
			c.add(source, line, "links", fmt.Sprintf("local link resolves outside repository: %q", target))
		}
		return
	}
	c.linkCount++
	if fragment == "" || anchorsExempt {
		return
	}
	anchors, err := c.anchors(destination)
	if err != nil {
		c.add(source, line, "anchors", fmt.Sprintf("cannot inspect anchor target %q: %v", target, err))
		return
	}
	if !anchors[fragment] {
		c.add(source, line, "anchors", fmt.Sprintf("missing anchor #%s in %s", fragment, c.repositoryRelative(destination)))
	}
}

func (c *checker) anchors(name string) (map[string]bool, error) {
	if cached, ok := c.anchorCache[name]; ok {
		return cached, nil
	}
	if !strings.EqualFold(filepath.Ext(name), ".md") {
		return nil, errors.New("anchor target is not Markdown")
	}
	content, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool)
	var fence fenceState
	previous := ""
	for index, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if updateFence(trimmed, &fence, index+1) {
			previous = ""
			continue
		}
		if fence.length != 0 {
			continue
		}
		for _, match := range explicitIDPattern.FindAllStringSubmatch(line, -1) {
			result[match[1]] = true
			c.anchorCount++
		}
		match := headingPattern.FindStringSubmatch(line)
		heading := ""
		if match != nil {
			heading = match[2]
		} else if setextPattern.MatchString(trimmed) && previous != "" {
			heading = previous
		}
		slug := githubSlug(heading)
		if slug == "" {
			if trimmed != "" {
				previous = trimmed
			} else {
				previous = ""
			}
			continue
		}
		base := slug
		for suffix := 1; result[slug]; suffix++ {
			slug = base + "-" + strconv.Itoa(suffix)
		}
		result[slug] = true
		c.anchorCount++
		previous = ""
	}
	c.anchorCache[name] = result
	return result, nil
}

func githubSlug(value string) string {
	value = markdownLinkTextPattern.ReplaceAllString(value, "$1")
	value = htmlTagPattern.ReplaceAllString(value, "")
	value = html.UnescapeString(strings.ReplaceAll(value, "`", ""))
	var result strings.Builder
	lastSpace := false
	for _, character := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(character), unicode.IsNumber(character), character == '-', character == '_':
			result.WriteRune(character)
			lastSpace = false
		case unicode.IsSpace(character):
			if result.Len() != 0 && !lastSpace {
				result.WriteByte('-')
				lastSpace = true
			}
		}
	}
	return strings.Trim(result.String(), "-")
}

func (c *checker) checkCurrentSection(section currentSection) {
	name := filepath.Join(c.moduleRoot, filepath.FromSlash(section.Path))
	if !within(c.moduleRoot, name) {
		c.add(name, 1, "issues", "current-section document escapes module")
		return
	}
	content, err := os.ReadFile(name)
	if err != nil {
		c.add(name, 1, "issues", fmt.Sprintf("cannot read current section: %v", err))
		return
	}
	directives := parseDirectives(string(content))
	if c.exempt(filepath.ToSlash(section.Path), "issues", directives) {
		return
	}
	lines := strings.Split(string(content), "\n")
	start, end, ok := sectionBounds(lines, section.Heading)
	if !ok {
		c.add(name, 1, "issues", fmt.Sprintf("current heading %q not found", section.Heading))
		return
	}
	allowed := make(map[int]bool, len(section.AllowedIssues))
	for _, issue := range section.AllowedIssues {
		allowed[issue] = true
	}
	var fence fenceState
	for index := start; index < end; index++ {
		line := strings.TrimSpace(lines[index])
		if updateFence(line, &fence, index+1) {
			continue
		}
		if fence.length != 0 {
			continue
		}
		line = stripInlineCode(line)
		seen := make(map[int]bool)
		for _, match := range issuePattern.FindAllStringSubmatch(line, -1) {
			issue, _ := strconv.Atoi(match[1])
			seen[issue] = true
			if !allowed[issue] {
				c.add(name, index+1, "issues", fmt.Sprintf("issue #%d is not allowed in explicitly current section %q", issue, section.Heading))
			}
		}
		for _, match := range issueURLPattern.FindAllStringSubmatch(line, -1) {
			issue, _ := strconv.Atoi(match[1])
			if seen[issue] || allowed[issue] {
				continue
			}
			c.add(name, index+1, "issues", fmt.Sprintf("issue #%d is not allowed in explicitly current section %q", issue, section.Heading))
		}
	}
}

func sectionBounds(lines []string, wanted string) (int, int, bool) {
	if strings.TrimSpace(wanted) == "" {
		return 0, len(lines), true
	}
	start, level := -1, 0
	var fence fenceState
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if updateFence(trimmed, &fence, index+1) || fence.length != 0 {
			continue
		}
		match := headingPattern.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		currentLevel := len(match[1])
		if start < 0 && strings.TrimSpace(match[2]) == wanted {
			start, level = index+1, currentLevel
			continue
		}
		if start >= 0 && currentLevel <= level {
			return start, index, true
		}
	}
	if start >= 0 {
		return start, len(lines), true
	}
	return 0, 0, false
}

func (c *checker) checkPathReference(reference pathReference) {
	name := filepath.Join(c.moduleRoot, filepath.FromSlash(reference.Document))
	if !within(c.moduleRoot, name) {
		c.add(name, 1, "links", "path-reference document escapes module")
		return
	}
	content, err := os.ReadFile(name)
	if err != nil {
		c.add(name, 1, "links", fmt.Sprintf("cannot read path-reference document: %v", err))
		return
	}
	line := lineContaining(string(content), reference.Text)
	if line == 0 {
		c.add(name, 1, "links", fmt.Sprintf("configured repository-path text %q is not documented", reference.Text))
		return
	}
	target := filepath.Clean(filepath.Join(c.repositoryRoot, filepath.FromSlash(reference.Target)))
	if !within(c.repositoryRoot, target) {
		c.add(name, line, "links", fmt.Sprintf("configured repository path escapes repository: %q", reference.Target))
		return
	}
	if _, err := os.Stat(target); err != nil {
		c.add(name, line, "links", fmt.Sprintf("missing configured repository path %q", reference.Target))
		return
	}
	if !withinResolved(c.repositoryRoot, target) {
		c.add(name, line, "links", fmt.Sprintf("configured repository path resolves outside repository: %q", reference.Target))
	}
}

func (c *checker) checkHelpCommand(item helpCommand) {
	name := filepath.Join(c.moduleRoot, filepath.FromSlash(item.Document))
	if !within(c.moduleRoot, name) {
		c.add(name, 1, "commands", "help document escapes module")
		return
	}
	content, err := os.ReadFile(name)
	if err != nil {
		c.add(name, 1, "commands", fmt.Sprintf("cannot read help document: %v", err))
		return
	}
	directives := parseDirectives(string(content))
	if c.exempt(filepath.ToSlash(item.Document), "commands", directives) {
		return
	}
	documented := strings.Join(item.Argv, " ")
	line := fencedLine(string(content), documented)
	if line == 0 {
		c.add(name, 1, "commands", fmt.Sprintf("configured help command is not documented exactly: %s", documented))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(item.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, item.Argv[0], item.Argv[1:]...)
	command.Dir = c.moduleRoot
	command.Env = offlineEnvironment(os.Environ())
	output := &boundedBuffer{limit: 64 * 1024}
	command.Stdout, command.Stderr = output, output
	err = command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		c.add(name, line, "commands", fmt.Sprintf("help command exceeded %ds timeout", item.TimeoutSeconds))
		return
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		c.add(name, line, "commands", fmt.Sprintf("help command failed offline: %s: %v", strings.TrimSpace(output.String()), err))
		return
	}
	if !strings.Contains(strings.ToLower(output.String()), strings.ToLower(item.Contains)) {
		c.add(name, line, "commands", fmt.Sprintf("help command output does not contain %q", item.Contains))
		return
	}
	c.helpCount++
}

func lineContaining(content, text string) int {
	for index, line := range strings.Split(content, "\n") {
		if strings.Contains(line, text) {
			return index + 1
		}
	}
	return 0
}

func fencedLine(content, wanted string) int {
	var fence fenceState
	for index, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if updateFence(trimmed, &fence, index+1) {
			continue
		}
		if fence.length != 0 && trimmed == wanted {
			return index + 1
		}
	}
	return 0
}

func (buffer *boundedBuffer) Write(payload []byte) (int, error) {
	written := len(payload)
	remaining := buffer.limit - buffer.Len()
	if remaining > 0 {
		if len(payload) > remaining {
			payload = payload[:remaining]
		}
		_, _ = buffer.Buffer.Write(payload)
	}
	return written, nil
}

func offlineEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "GOPROXY=") || strings.HasPrefix(upper, "GOSUMDB=") ||
			strings.HasPrefix(upper, "HTTP_PROXY=") || strings.HasPrefix(upper, "HTTPS_PROXY=") ||
			strings.HasPrefix(upper, "ALL_PROXY=") || strings.HasPrefix(upper, "NO_PROXY=") ||
			strings.HasPrefix(upper, "DICOMGO_") || strings.HasPrefix(upper, "PACS_") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "GOPROXY=off", "GOSUMDB=off")
}

func parseDirectives(content string) map[string]bool {
	result := make(map[string]bool)
	lines := strings.Split(content, "\n")
	if len(lines) > 20 {
		lines = lines[:20]
	}
	for _, line := range lines {
		match := directivePattern.FindStringSubmatch(line)
		if match == nil || strings.TrimSpace(match[2]) == "" {
			continue
		}
		for _, rule := range strings.Split(match[1], ",") {
			if knownRule(rule) {
				result[rule] = true
			}
		}
	}
	return result
}

func (c *checker) exempt(relative, rule string, directives map[string]bool) bool {
	if directives[rule] {
		return true
	}
	relative = filepath.ToSlash(relative)
	for _, item := range c.configuration.Exemptions {
		if !matchesPattern(item.Pattern, relative) {
			continue
		}
		for _, candidate := range item.Rules {
			if candidate == rule {
				return true
			}
		}
	}
	return false
}

func matchesPattern(pattern, name string) bool {
	pattern = filepath.ToSlash(pattern)
	if strings.HasSuffix(pattern, "/**") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "**"))
	}
	matched, _ := path.Match(pattern, name)
	return matched
}

func within(root, name string) bool {
	relative, err := filepath.Rel(root, name)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func withinResolved(root, name string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedName, err := filepath.EvalSymlinks(name)
	if err != nil {
		return false
	}
	return within(resolvedRoot, resolvedName)
}

func (c *checker) add(name string, line int, rule, message string) {
	c.diagnostics = append(c.diagnostics, diagnostic{
		Path:    c.repositoryRelative(name),
		Line:    line,
		Rule:    rule,
		Message: message,
	})
}

func (c *checker) moduleRelative(name string) string {
	relative, err := filepath.Rel(c.moduleRoot, name)
	if err != nil {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(relative)
}

func (c *checker) repositoryRelative(name string) string {
	relative, err := filepath.Rel(c.repositoryRoot, name)
	if err != nil {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(relative)
}
