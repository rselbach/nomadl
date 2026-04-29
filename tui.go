package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/cellbuf"
)

type screen int

const (
	screenServices screen = iota
	screenLogs
)

type serviceItem struct {
	service serviceSummary
}

func (item serviceItem) FilterValue() string {
	return item.service.Name
}

func (item serviceItem) Title() string {
	return item.service.Name
}

func (item serviceItem) Description() string {
	label := "registrations"
	if item.service.Source == "job" {
		label = "running tasks"
	}
	parts := []string{fmt.Sprintf("%d %s", item.service.Instances, label)}
	if item.service.Source != "" {
		parts = append(parts, item.service.Source)
	}
	if item.service.Type != "" {
		parts = append(parts, item.service.Type)
	}
	if item.service.Status != "" {
		parts = append(parts, item.service.Status)
	}
	if item.service.Provider != "" {
		parts = append(parts, "provider "+item.service.Provider)
	}
	if len(item.service.Tags) > 0 {
		parts = append(parts, "tags "+strings.Join(item.service.Tags, ", "))
	}
	return strings.Join(parts, " | ")
}

type app struct {
	client *nomadClient
	config appConfig

	screen   screen
	services list.Model
	logs     viewport.Model
	search   textinput.Model
	spinner  spinner.Model

	width  int
	height int

	loadingServices bool
	loadingLogs     bool
	selectedService string
	selectedTarget  serviceSummary
	logMessages     <-chan tea.Msg
	logCancel       context.CancelFunc
	follow          bool
	wrapLogs        bool
	highlightJSON   bool
	searching       bool
	searchQuery     string
	lineCount       int
	logBuffer       []string
	lastError       string
}

type servicesLoadedMsg struct {
	services []serviceSummary
	err      error
}

type instancesLoadedMsg struct {
	service   string
	instances []serviceInstance
	err       error
}

type logLineMsg logLine

type streamErrorMsg struct {
	source logSource
	err    error
}

type logsStoppedMsg struct{}

type refreshServicesMsg struct{}

func newApp(client *nomadClient, config appConfig) app {
	items := []list.Item{}
	services := list.New(items, list.NewDefaultDelegate(), 0, 0)
	services.Title = "Nomad services"
	services.SetShowStatusBar(false)
	services.SetFilteringEnabled(true)
	services.SetShowHelp(false)

	spin := spinner.New()
	spin.Spinner = spinner.Dot
	logs := viewport.New(0, 0)
	logs.SetHorizontalStep(12)
	search := textinput.New()
	search.Prompt = "/"
	search.Placeholder = "filter logs"

	return app{
		client:          client,
		config:          config,
		screen:          screenServices,
		services:        services,
		logs:            logs,
		search:          search,
		spinner:         spin,
		loadingServices: true,
		follow:          true,
		highlightJSON:   true,
	}
}

func (app app) Init() tea.Cmd {
	return tea.Batch(app.spinner.Tick, app.loadServices(), refreshServicesAfter(app.config.refreshInterval))
}

func (app app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		app.width = msg.Width
		app.height = msg.Height
		app.resize()
		app.renderLogs()
	case tea.KeyMsg:
		if app.screen == screenLogs && app.searching {
			switch msg.String() {
			case "ctrl+c":
				app.stopLogs()
				return app, tea.Quit
			case "enter":
				app.searching = false
				app.search.Blur()
				app.searchQuery = app.search.Value()
				app.renderLogs()
				return app, tea.Batch(commands...)
			case "esc":
				app.searching = false
				app.search.SetValue(app.searchQuery)
				app.search.Blur()
				return app, tea.Batch(commands...)
			}

			var command tea.Cmd
			app.search, command = app.search.Update(msg)
			app.searchQuery = app.search.Value()
			app.renderLogs()
			commands = append(commands, command)
			return app, tea.Batch(commands...)
		}

		switch msg.String() {
		case "ctrl+c", "q":
			app.stopLogs()
			return app, tea.Quit
		case "esc":
			if app.screen == screenLogs {
				app.stopLogs()
				app.screen = screenServices
				app.selectedService = ""
				app.selectedTarget = serviceSummary{}
				app.searching = false
				app.searchQuery = ""
				app.search.SetValue("")
				app.search.Blur()
				app.lastError = ""
				commands = append(commands, app.loadServices())
				return app, tea.Batch(commands...)
			}
		case "enter":
			if app.screen == screenServices {
				item, ok := app.services.SelectedItem().(serviceItem)
				if ok {
					app.screen = screenLogs
					app.selectedService = item.service.Name
					app.selectedTarget = item.service
					app.logs.SetContent("")
					app.logs.GotoBottom()
					app.loadingLogs = true
					app.lineCount = 0
					app.logBuffer = nil
					app.searching = false
					app.searchQuery = ""
					app.search.SetValue("")
					app.search.Blur()
					app.lastError = ""
					commands = append(commands, app.loadInstances(item.service))
				}
			}
		case "f":
			if app.screen == screenLogs {
				app.follow = !app.follow
				if app.follow {
					app.logs.GotoBottom()
				}
			}
		case "/":
			if app.screen == screenLogs {
				app.searching = true
				app.search.SetValue(app.searchQuery)
				app.search.Focus()
				commands = append(commands, textinput.Blink)
			}
		case "w":
			if app.screen == screenLogs {
				app.wrapLogs = !app.wrapLogs
				app.logs.SetXOffset(0)
				app.renderLogs()
			}
		case "J":
			if app.screen == screenLogs {
				app.highlightJSON = !app.highlightJSON
				app.renderLogs()
			}
		case "shift+left", "H":
			if app.screen == screenLogs && !app.wrapLogs {
				app.logs.ScrollLeft(app.logs.Width / 2)
			}
		case "shift+right", "L":
			if app.screen == screenLogs && !app.wrapLogs {
				app.logs.ScrollRight(app.logs.Width / 2)
			}
		case "r":
			if app.screen == screenServices {
				app.loadingServices = true
				app.lastError = ""
				commands = append(commands, app.loadServices())
			}
		}
	case servicesLoadedMsg:
		app.loadingServices = false
		if msg.err != nil {
			app.lastError = msg.err.Error()
			break
		}
		app.lastError = ""
		items := make([]list.Item, 0, len(msg.services))
		for _, service := range msg.services {
			items = append(items, serviceItem{service: service})
		}
		commands = append(commands, app.services.SetItems(items))
	case instancesLoadedMsg:
		app.loadingLogs = false
		if msg.err != nil {
			app.lastError = msg.err.Error()
			break
		}
		if len(msg.instances) == 0 {
			app.lastError = fmt.Sprintf("service %q has no task allocations with logs", msg.service)
			break
		}
		commands = append(commands, app.startStreams(msg.service, msg.instances))
	case logLineMsg:
		line := logLine(msg)
		if line.Source.Service == app.selectedService {
			app.appendLine(line)
		}
		if app.screen == screenLogs && app.logMessages != nil {
			commands = append(commands, waitForLogMessage(app.logMessages))
		}
	case streamErrorMsg:
		if msg.err != nil && app.selectedService == msg.source.Service {
			app.appendSystemLine(fmt.Sprintf("stream error [%s %s %s]: %v", shortAlloc(msg.source.AllocID), msg.source.Task, msg.source.Stream, msg.err))
		}
		if app.screen == screenLogs && app.logMessages != nil {
			commands = append(commands, waitForLogMessage(app.logMessages))
		}
	case logsStoppedMsg:
	case refreshServicesMsg:
		commands = append(commands, refreshServicesAfter(app.config.refreshInterval))
		if app.screen == screenServices && !app.loadingServices {
			commands = append(commands, app.loadServices())
		}
	}

	var command tea.Cmd
	if app.screen == screenLogs {
		app.logs, command = app.logs.Update(msg)
	} else {
		app.services, command = app.services.Update(msg)
	}
	commands = append(commands, command)

	app.spinner, command = app.spinner.Update(msg)
	commands = append(commands, command)

	return app, tea.Batch(commands...)
}

func (app app) View() string {
	switch app.screen {
	case screenLogs:
		return app.logsView()
	default:
		return app.servicesView()
	}
}

func (app *app) resize() {
	headerHeight := 3
	footerHeight := 2
	contentHeight := app.height - headerHeight - footerHeight
	if contentHeight < 1 {
		contentHeight = 1
	}

	app.services.SetSize(app.width, app.height-footerHeight)
	app.logs.Width = app.width
	app.logs.Height = contentHeight
}

func (app app) servicesView() string {
	status := "enter: view logs | /: filter | r: refresh | q: quit"
	if app.loadingServices {
		status = app.spinner.View() + " loading services | " + status
	}
	if app.lastError != "" {
		status = errorStyle.Render(app.lastError) + "\n" + status
	}
	return lipgloss.JoinVertical(lipgloss.Left, app.services.View(), footerStyle.Width(app.width).Render(status))
}

func (app app) logsView() string {
	follow := "off"
	if app.follow {
		follow = "on"
	}

	header := titleStyle.Render(app.selectedService)
	xScroll := math.Round(app.logs.HorizontalScrollPercent() * 100)
	wrap := "off"
	if app.wrapLogs {
		wrap = "on"
	}
	jsonHighlight := "off"
	if app.highlightJSON {
		jsonHighlight = "on"
	}
	search := "off"
	if app.searchQuery != "" {
		search = fmt.Sprintf("%q", app.searchQuery)
	}
	horizontalHelp := "left/right: horizontal | H/L: fast horizontal"
	if app.wrapLogs {
		horizontalHelp = "horizontal disabled while wrapped"
	}
	meta := subtleStyle.Render(fmt.Sprintf("%d lines | follow %s | wrap %s | json %s | search %s | x %.0f%% | esc: services | /: search | f: follow | w: wrap | J: JSON | up/down/pg: vertical | %s | q: quit", app.lineCount, follow, wrap, jsonHighlight, search, xScroll, horizontalHelp))
	if app.searching {
		meta = subtleStyle.Render("search: ") + app.search.View() + subtleStyle.Render(" | enter: apply | esc: cancel")
	}
	if app.loadingLogs {
		meta = app.spinner.View() + " resolving allocations | " + meta
	}
	if app.lastError != "" {
		meta = errorStyle.Render(app.lastError) + "\n" + meta
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		meta,
		app.logs.View(),
	)
}

func (app app) loadServices() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		services, err := app.client.ListServices(ctx)
		return servicesLoadedMsg{services: services, err: err}
	}
}

func (app app) loadInstances(target serviceSummary) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		instances, err := app.client.TargetInstances(ctx, target)
		return instancesLoadedMsg{service: target.Name, instances: instances, err: err}
	}
}

func (app *app) startStreams(service string, instances []serviceInstance) tea.Cmd {
	app.stopLogs()

	ctx, cancel := context.WithCancel(context.Background())
	app.logCancel = cancel
	messages := make(chan tea.Msg, 256)
	app.logMessages = messages

	sources := app.logSources(service, instances)
	var streams sync.WaitGroup
	streams.Add(len(sources))
	for _, source := range sources {
		source := source
		go func() {
			defer streams.Done()
			app.streamSource(ctx, source, messages)
		}()
	}
	go func() {
		streams.Wait()
		close(messages)
	}()

	return waitForLogMessage(messages)
}

func (app app) streamSource(ctx context.Context, source logSource, messages chan<- tea.Msg) {
	lines := make(chan logLine, 256)
	done := make(chan error, 1)

	go func() {
		done <- app.client.StreamLogs(ctx, source, app.config.tailBytes, lines)
	}()

	for {
		select {
		case line := <-lines:
			if !sendLogMessage(ctx, messages, logLineMsg(line)) {
				return
			}
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				sendLogMessage(ctx, messages, streamErrorMsg{source: source, err: err})
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

func (app app) logSources(service string, instances []serviceInstance) []logSource {
	streams := []string{"stdout", "stderr"}
	if app.config.logType != "both" {
		streams = []string{app.config.logType}
	}

	sources := make([]logSource, 0, len(instances)*len(streams))
	for _, instance := range instances {
		for _, stream := range streams {
			sources = append(sources, logSource{
				Service: service,
				AllocID: instance.AllocID,
				JobID:   instance.JobID,
				Task:    instance.Task,
				Stream:  stream,
			})
		}
	}
	return sources
}

func (app *app) stopLogs() {
	if app.logCancel == nil {
		return
	}
	app.logCancel()
	app.logCancel = nil
	app.logMessages = nil
}

func (app *app) appendLine(line logLine) {
	prefix := fmt.Sprintf("[%s %s %s]", shortAlloc(line.Source.AllocID), line.Source.Task, line.Source.Stream)
	app.appendRawLine(logLineStyle.Render(prefix) + " " + line.Text)
}

func (app *app) appendSystemLine(line string) {
	app.appendRawLine(errorStyle.Render(line))
}

func (app *app) appendRawLine(line string) {
	app.logBuffer = append(app.logBuffer, line)
	if len(app.logBuffer) > app.config.maxLines {
		copy(app.logBuffer, app.logBuffer[len(app.logBuffer)-app.config.maxLines:])
		app.logBuffer = app.logBuffer[:app.config.maxLines]
	}
	app.renderLogs()
	app.lineCount++
	if app.follow {
		app.logs.GotoBottom()
	}
}

func (app *app) renderLogs() {
	content := strings.Join(app.renderedLogs(), "\n")
	if app.wrapLogs && app.logs.Width > 0 {
		content = cellbuf.Wrap(content, app.logs.Width, "")
	}
	app.logs.SetContent(content)
	if app.follow {
		app.logs.GotoBottom()
	}
}

func (app app) renderedLogs() []string {
	logs := app.filteredLogs()
	if !app.highlightJSON {
		return logs
	}

	rendered := make([]string, 0, len(logs))
	for _, line := range logs {
		rendered = append(rendered, highlightJSONLogLine(line))
	}
	return rendered
}

func (app app) filteredLogs() []string {
	query := strings.TrimSpace(app.searchQuery)
	if query == "" {
		return app.logBuffer
	}

	logs := make([]string, 0, len(app.logBuffer))
	for _, line := range app.logBuffer {
		if logMatchesSearch(line, query) {
			logs = append(logs, line)
		}
	}
	return logs
}

func logMatchesSearch(line string, query string) bool {
	field, value, ok := parseJSONFieldSearch(query)
	if ok {
		return logJSONFieldMatches(line, field, value)
	}
	return valueMatchesSearch(line, query)
}

func parseJSONFieldSearch(query string) (string, string, bool) {
	if !strings.HasPrefix(query, "@") {
		return "", "", false
	}
	field, value, found := strings.Cut(strings.TrimPrefix(query, "@"), ":")
	field = strings.TrimSpace(field)
	if !found || field == "" {
		return "", "", false
	}
	return field, strings.TrimSpace(value), true
}

func logJSONFieldMatches(line string, field string, want string) bool {
	plain := ansi.Strip(line)
	jsonStart, jsonEnd := jsonRange(plain)
	if jsonStart < 0 {
		return false
	}

	var payload any
	if err := json.Unmarshal([]byte(plain[jsonStart:jsonEnd]), &payload); err != nil {
		return false
	}

	value, ok := jsonFieldValue(payload, field)
	if !ok {
		return false
	}
	valueString := jsonValueString(value)
	pattern, ok := wildcardPattern(want)
	if ok {
		return wildcardContains(valueString, pattern)
	}
	return valueString == want
}

func valueMatchesSearch(value string, query string) bool {
	pattern, ok := wildcardPattern(query)
	if ok {
		return wildcardContains(value, pattern)
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(query))
}

func wildcardPattern(query string) (string, bool) {
	if len(query) < 2 || !strings.HasPrefix(query, "/") || !strings.HasSuffix(query, "/") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(query, "/"), "/"), true
}

func wildcardContains(value string, pattern string) bool {
	value = strings.ToLower(value)
	pattern = strings.ToLower(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}

	parts := strings.Split(pattern, "*")
	position := 0
	for _, part := range parts {
		if part == "" {
			continue
		}

		match := strings.Index(value[position:], part)
		if match < 0 {
			return false
		}
		position += match + len(part)
	}
	return true
}

func jsonFieldValue(payload any, field string) (any, bool) {
	current := payload
	for _, part := range strings.Split(field, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = lookupJSONFieldPart(object, part)
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func lookupJSONFieldPart(object map[string]any, part string) (any, bool) {
	for _, candidate := range jsonFieldCandidates(part) {
		value, ok := object[candidate]
		if ok {
			return value, true
		}
	}
	return nil, false
}

func jsonFieldCandidates(part string) []string {
	candidates := []string{part}
	if !strings.HasPrefix(part, "@") {
		candidates = append(candidates, "@"+part)
	}

	switch part {
	case "msg":
		candidates = append(candidates, "message", "@message")
	case "err":
		candidates = append(candidates, "error", "@error")
	}

	return candidates
}

func jsonValueString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case nil:
		return "null"
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(encoded)
	}
}

func highlightJSONLogLine(line string) string {
	plain := ansi.Strip(line)
	jsonStart, _ := jsonRange(plain)
	if jsonStart < 0 {
		return line
	}

	styledStart := styledByteOffset(line, jsonStart)
	if styledStart < 0 {
		return line
	}
	return line[:styledStart] + highlightJSON(line[styledStart:])
}

func jsonStartOffset(line string) int {
	start, _ := jsonRange(line)
	return start
}

func jsonRange(line string) (int, int) {
	for offset := 0; offset < len(line); offset++ {
		if line[offset] != '{' && line[offset] != '[' {
			continue
		}

		var payload any
		decoder := json.NewDecoder(strings.NewReader(line[offset:]))
		if err := decoder.Decode(&payload); err == nil {
			return offset, offset + int(decoder.InputOffset())
		}
	}
	return -1, -1
}

func styledByteOffset(styled string, plainOffset int) int {
	plainIndex := 0
	for index := 0; index < len(styled); index++ {
		if styled[index] == '\x1b' {
			index = skipANSISequence(styled, index)
			continue
		}
		if plainIndex == plainOffset {
			return index
		}
		plainIndex++
	}
	if plainIndex == plainOffset {
		return len(styled)
	}
	return -1
}

func skipANSISequence(value string, start int) int {
	for index := start + 1; index < len(value); index++ {
		char := value[index]
		if char >= '@' && char <= '~' {
			return index
		}
	}
	return len(value) - 1
}

func highlightJSON(value string) string {
	var builder strings.Builder
	inString := false
	escaped := false
	stringStart := 0

	for index := 0; index < len(value); index++ {
		char := value[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				segment := value[stringStart : index+1]
				if isJSONKey(value, index+1) {
					builder.WriteString(jsonKeyStyle.Render(segment))
				} else {
					builder.WriteString(jsonStringStyle.Render(segment))
				}
				inString = false
			}
			continue
		}

		if char == '"' {
			inString = true
			stringStart = index
			continue
		}

		if isJSONNumberStart(value, index) {
			end := consumeJSONNumber(value, index)
			builder.WriteString(jsonNumberStyle.Render(value[index:end]))
			index = end - 1
			continue
		}

		switch {
		case strings.HasPrefix(value[index:], "true"):
			builder.WriteString(jsonBoolStyle.Render("true"))
			index += len("true") - 1
		case strings.HasPrefix(value[index:], "false"):
			builder.WriteString(jsonBoolStyle.Render("false"))
			index += len("false") - 1
		case strings.HasPrefix(value[index:], "null"):
			builder.WriteString(jsonNullStyle.Render("null"))
			index += len("null") - 1
		case strings.ContainsRune("{}[]:,", rune(char)):
			builder.WriteString(jsonPunctuationStyle.Render(string(char)))
		default:
			builder.WriteByte(char)
		}
	}

	if inString {
		builder.WriteString(value[stringStart:])
	}
	return builder.String()
}

func isJSONKey(value string, offset int) bool {
	for index := offset; index < len(value); index++ {
		switch value[index] {
		case ' ', '\t', '\r', '\n':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}

func isJSONNumberStart(value string, index int) bool {
	char := value[index]
	if char >= '0' && char <= '9' {
		return true
	}
	return char == '-' && index+1 < len(value) && value[index+1] >= '0' && value[index+1] <= '9'
}

func consumeJSONNumber(value string, start int) int {
	index := start
	for index < len(value) {
		char := value[index]
		if (char >= '0' && char <= '9') || char == '-' || char == '+' || char == '.' || char == 'e' || char == 'E' {
			index++
			continue
		}
		break
	}
	return index
}

func waitForLogMessage(messages <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		message, ok := <-messages
		if !ok {
			return logsStoppedMsg{}
		}
		return message
	}
}

func sendLogMessage(ctx context.Context, messages chan<- tea.Msg, message tea.Msg) bool {
	select {
	case messages <- message:
		return true
	case <-ctx.Done():
		return false
	}
}

func refreshServicesAfter(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return refreshServicesMsg{}
	})
}

func shortAlloc(allocID string) string {
	if len(allocID) <= 8 {
		return allocID
	}
	return allocID[:8]
}

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	subtleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	footerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	logLineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))

	jsonKeyStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	jsonStringStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	jsonNumberStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	jsonBoolStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("219"))
	jsonNullStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	jsonPunctuationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
)
