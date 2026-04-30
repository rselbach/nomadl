package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
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
	service    serviceSummary
	nameWidth  int
	statWidths serviceStatWidths
}

func (item serviceItem) FilterValue() string {
	return item.service.Name
}

func (item serviceItem) Title() string {
	stats := item.stats()
	if stats == "" {
		return item.service.Name
	}
	return item.paddedName() + "  " + stats
}

func (item serviceItem) Description() string {
	return item.stats()
}

func (item serviceItem) stats() string {
	return strings.Join(item.statParts(false), " | ")
}

func (item serviceItem) paddedName() string {
	name := item.service.Name
	if item.nameWidth <= 0 {
		return name
	}

	padding := item.nameWidth - lipgloss.Width(name)
	if padding <= 0 {
		return name
	}
	return name + strings.Repeat(" ", padding)
}

func (item serviceItem) styledStats() string {
	parts := item.statParts(true)
	return strings.Join(parts, serviceSeparatorStyle.Render(" | "))
}

func (item serviceItem) countStat() string {
	count := fmt.Sprintf("%d", item.service.Instances)
	if item.statWidths.count > 0 {
		count = leftPad(count, item.statWidths.count)
	}

	label := item.countLabel()
	if item.statWidths.countLabel > 0 {
		label = rightPad(label, item.statWidths.countLabel)
	}
	return count + " " + label
}

func (item serviceItem) statParts(styled bool) []string {
	parts := []string{item.styleStat(item.countStat(), serviceCountStyle, styled)}

	if item.service.Source != "" || item.statWidths.source > 0 {
		parts = append(parts, item.styleStat(rightPad(item.service.Source, item.statWidths.source), serviceSourceStyle, styled))
	}
	if item.service.Type != "" || item.statWidths.typ > 0 {
		parts = append(parts, item.styleStat(rightPad(item.service.Type, item.statWidths.typ), serviceTypeStyle, styled))
	}
	if item.service.Status != "" || item.statWidths.status > 0 {
		status := rightPad(item.service.Status, item.statWidths.status)
		parts = append(parts, item.styleStat(status, serviceStatusStyle(item.service.Status), styled))
	}
	if item.service.Provider != "" || item.statWidths.provider > 0 {
		parts = append(parts, item.styleStat(rightPad(item.providerStat(), item.statWidths.provider), serviceProviderStyle, styled))
	}
	if len(item.service.Tags) > 0 {
		parts = append(parts, item.styleStat("tags "+strings.Join(item.service.Tags, ", "), serviceTagsStyle, styled))
	}
	return parts
}

func (item serviceItem) providerStat() string {
	if item.service.Provider == "" {
		return ""
	}
	return "provider " + item.service.Provider
}

func (item serviceItem) styleStat(value string, style lipgloss.Style, styled bool) string {
	if !styled {
		return value
	}
	return style.Render(value)
}

func serviceNameWidth(services []serviceSummary) int {
	width := 0
	for _, service := range services {
		width = max(width, lipgloss.Width(service.Name))
	}
	return width
}

type serviceStatWidths struct {
	count      int
	countLabel int
	source     int
	typ        int
	status     int
	provider   int
}

func serviceStatColumnWidths(services []serviceSummary) serviceStatWidths {
	var widths serviceStatWidths
	for _, service := range services {
		item := serviceItem{service: service}
		widths.count = max(widths.count, lipgloss.Width(fmt.Sprintf("%d", service.Instances)))
		widths.countLabel = max(widths.countLabel, lipgloss.Width(item.countLabel()))
		widths.source = max(widths.source, lipgloss.Width(service.Source))
		widths.typ = max(widths.typ, lipgloss.Width(service.Type))
		widths.status = max(widths.status, lipgloss.Width(service.Status))
		widths.provider = max(widths.provider, lipgloss.Width(item.providerStat()))
	}
	return widths
}

func (item serviceItem) countLabel() string {
	if item.service.Source == "job" {
		return "running tasks"
	}
	return "registrations"
}

func leftPad(value string, width int) string {
	padding := width - lipgloss.Width(value)
	if padding <= 0 {
		return value
	}
	return strings.Repeat(" ", padding) + value
}

func rightPad(value string, width int) string {
	padding := width - lipgloss.Width(value)
	if padding <= 0 {
		return value
	}
	return value + strings.Repeat(" ", padding)
}

func serviceStatusStyle(status string) lipgloss.Style {
	switch strings.ToLower(status) {
	case "passing", "running", "healthy", "ready", "complete":
		return serviceStatusGoodStyle
	case "pending", "warning", "starting":
		return serviceStatusWarnStyle
	case "critical", "dead", "failed", "failing", "lost":
		return serviceStatusBadStyle
	default:
		return serviceStatusDefaultStyle
	}
}

type serviceDelegate struct{}

func newServiceDelegate() serviceDelegate {
	return serviceDelegate{}
}

func (serviceDelegate) Height() int {
	return 1
}

func (serviceDelegate) Spacing() int {
	return 0
}

func (serviceDelegate) Update(tea.Msg, *list.Model) tea.Cmd {
	return nil
}

func (serviceDelegate) Render(writer io.Writer, model list.Model, index int, item list.Item) {
	service, ok := item.(serviceItem)
	if !ok {
		return
	}

	nameStyle := serviceNameStyle
	prefix := "  "
	if index == model.Index() && model.FilterState() != list.Filtering {
		nameStyle = serviceSelectedNameStyle
		prefix = serviceSelectedMarkerStyle.Render("> ")
	}

	row := prefix + nameStyle.Render(service.paddedName()) + "  " + service.styledStats()
	width := model.Width()
	if width > 0 {
		row = ansi.Truncate(row, width, "...")
	}
	fmt.Fprint(writer, row)
}

type app struct {
	client *nomadClient
	store  *appStore
	config appConfig

	screen   screen
	services list.Model
	logs     viewport.Model
	search   textinput.Model
	spinner  spinner.Model

	width  int
	height int

	loadingServices   bool
	loadingLogs       bool
	refreshingLogs    bool
	selectedService   string
	selectedTarget    serviceSummary
	selectedInstances []serviceInstance
	logMessages       chan tea.Msg
	activeLogStreams  map[string]context.CancelFunc
	logGeneration     int
	follow            bool
	wrapLogs          bool
	highlightJSON     bool
	searching         bool
	showHelp          bool
	searchQuery       string
	searchCacheQuery  string
	searchCacheExpr   searchExpression
	searchCacheErr    error
	searchCacheValid  bool
	searchHistory     []string
	searchHistoryPos  int
	searchDraft       string
	lineCount         int
	logBuffer         []string
	renderCache       []string
	renderCacheQuery  string
	renderCacheJSON   bool
	renderCacheLen    int
	renderCacheValid  bool
	lastError         string
}

type servicesLoadedMsg struct {
	services []serviceSummary
	err      error
}

type instancesLoadedMsg struct {
	service   string
	instances []serviceInstance
	refresh   bool
	err       error
}

type logLineMsg struct {
	line       logLine
	generation int
}

type streamErrorMsg struct {
	source     logSource
	generation int
	err        error
}

type logStreamStoppedMsg struct {
	source     logSource
	generation int
}

type logsStoppedMsg struct{}

type refreshServicesMsg struct{}

type searchHistoryLoadedMsg struct {
	queries []string
	err     error
}

type searchHistorySavedMsg struct {
	err error
}

type preferencesSavedMsg struct {
	err error
}

func newApp(client *nomadClient, config appConfig, store *appStore) app {
	preferences := config.preferences
	if !config.preferencesSet {
		preferences = defaultAppPreferences()
		if config.logType != "" {
			preferences.logType = config.logType
		}
	}
	if preferences.logType == "" {
		preferences.logType = "stderr"
	}
	config.logType = preferences.logType

	items := []list.Item{}
	services := list.New(items, newServiceDelegate(), 0, 0)
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
		client:           client,
		store:            store,
		config:           config,
		screen:           screenServices,
		services:         services,
		logs:             logs,
		search:           search,
		spinner:          spin,
		loadingServices:  true,
		follow:           preferences.follow,
		wrapLogs:         preferences.wrapLogs,
		highlightJSON:    preferences.highlightJSON,
		searchHistoryPos: -1,
	}
}

func (app app) Init() tea.Cmd {
	return tea.Batch(app.spinner.Tick, app.loadServices(), app.loadSearchHistory(), refreshServicesAfter(app.config.refreshInterval))
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
				app.setSearchQuery(app.search.Value())
				app.rememberSearch(app.searchQuery)
				app.renderLogs()
				commands = append(commands, app.saveSearchHistory(app.searchQuery))
				return app, tea.Batch(commands...)
			case "esc":
				app.searching = false
				app.search.SetValue(app.searchQuery)
				app.search.Blur()
				return app, tea.Batch(commands...)
			case "up", "ctrl+p":
				app.previousSearch()
				app.renderLogs()
				return app, tea.Batch(commands...)
			case "down", "ctrl+n":
				app.nextSearch()
				app.renderLogs()
				return app, tea.Batch(commands...)
			}

			var command tea.Cmd
			app.search, command = app.search.Update(msg)
			app.searchHistoryPos = -1
			app.searchDraft = app.search.Value()
			app.setSearchQuery(app.search.Value())
			app.renderLogs()
			commands = append(commands, command)
			return app, tea.Batch(commands...)
		}

		if app.screen == screenLogs && app.showHelp {
			switch msg.String() {
			case "ctrl+c", "q":
				app.stopLogs()
				return app, tea.Quit
			case "esc", "?", "h":
				app.showHelp = false
				return app, tea.Batch(commands...)
			default:
				return app, tea.Batch(commands...)
			}
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
				app.selectedInstances = nil
				app.searching = false
				app.showHelp = false
				app.setSearchQuery("")
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
					app.selectedInstances = nil
					app.logs.SetContent("")
					app.logs.GotoBottom()
					app.loadingLogs = true
					app.refreshingLogs = false
					app.lineCount = 0
					app.clearLogBuffer()
					app.searching = false
					app.showHelp = false
					app.setSearchQuery("")
					app.search.SetValue("")
					app.search.Blur()
					app.lastError = ""
					commands = append(commands, app.loadInstances(item.service, false))
				}
			}
		case "f":
			if app.screen == screenLogs {
				app.follow = !app.follow
				if app.follow {
					app.logs.GotoBottom()
				}
				commands = append(commands, app.savePreferences())
			}
		case "/":
			if app.screen == screenLogs {
				app.searching = true
				app.showHelp = false
				app.searchHistoryPos = -1
				app.searchDraft = app.searchQuery
				app.search.SetValue(app.searchQuery)
				app.search.Focus()
				commands = append(commands, app.loadSearchHistory(), textinput.Blink)
			}
		case "?", "h":
			if app.screen == screenLogs {
				app.showHelp = true
			}
		case "w":
			if app.screen == screenLogs {
				app.wrapLogs = !app.wrapLogs
				app.logs.SetXOffset(0)
				app.renderLogs()
				commands = append(commands, app.savePreferences())
			}
		case "J":
			if app.screen == screenLogs {
				app.highlightJSON = !app.highlightJSON
				app.invalidateRenderCache()
				app.renderLogs()
				commands = append(commands, app.savePreferences())
			}
		case "s":
			if app.screen == screenLogs {
				commands = append(commands, app.toggleLogStream())
				commands = append(commands, app.savePreferences())
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
		nameWidth := serviceNameWidth(msg.services)
		statWidths := serviceStatColumnWidths(msg.services)
		for _, service := range msg.services {
			items = append(items, serviceItem{service: service, nameWidth: nameWidth, statWidths: statWidths})
		}
		commands = append(commands, app.services.SetItems(items))
	case instancesLoadedMsg:
		if msg.refresh {
			app.refreshingLogs = false
		} else {
			app.loadingLogs = false
		}
		if msg.err != nil {
			app.lastError = msg.err.Error()
			break
		}
		if len(msg.instances) == 0 {
			app.lastError = fmt.Sprintf("service %q has no task allocations with logs", msg.service)
			app.reconcileLogStreams(msg.service, nil)
			break
		}
		app.lastError = ""
		if msg.refresh {
			app.reconcileLogStreams(msg.service, msg.instances)
		} else {
			commands = append(commands, app.startStreams(msg.service, msg.instances))
		}
	case logLineMsg:
		line := msg.line
		if msg.generation == app.logGeneration && line.Source.Service == app.selectedService {
			app.appendLine(line)
		}
		if app.screen == screenLogs && app.logMessages != nil {
			commands = append(commands, waitForLogMessage(app.logMessages))
		}
	case streamErrorMsg:
		if msg.generation == app.logGeneration && msg.err != nil && app.selectedService == msg.source.Service {
			app.removeLogStream(msg.source)
			app.appendSystemLine(fmt.Sprintf("stream error [%s %s %s]: %v", shortAlloc(msg.source.AllocID), msg.source.Task, msg.source.Stream, msg.err))
		}
		if app.screen == screenLogs && app.logMessages != nil {
			commands = append(commands, waitForLogMessage(app.logMessages))
		}
	case logStreamStoppedMsg:
		if msg.generation == app.logGeneration {
			app.removeLogStream(msg.source)
		}
		if app.screen == screenLogs && app.logMessages != nil {
			commands = append(commands, waitForLogMessage(app.logMessages))
		}
	case logsStoppedMsg:
	case searchHistoryLoadedMsg:
		if msg.err != nil {
			app.lastError = msg.err.Error()
			break
		}
		app.searchHistory = mergeSearchHistory(msg.queries, app.searchHistory)
	case searchHistorySavedMsg:
		if msg.err != nil {
			app.lastError = msg.err.Error()
		}
	case preferencesSavedMsg:
		if msg.err != nil {
			app.lastError = msg.err.Error()
		}
	case refreshServicesMsg:
		commands = append(commands, refreshServicesAfter(app.config.refreshInterval))
		if app.screen == screenServices && !app.loadingServices {
			commands = append(commands, app.loadServices())
		}
		if app.screen == screenLogs && app.selectedTarget.Name != "" && !app.loadingLogs && !app.refreshingLogs {
			app.refreshingLogs = true
			commands = append(commands, app.loadInstances(app.selectedTarget, true))
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
	meta := subtleStyle.Render(fmt.Sprintf("%d lines | stream %s | follow %s | wrap %s | json %s | search %s | x %.0f%% | ?/h: help", app.lineCount, app.config.logType, follow, wrap, jsonHighlight, search, xScroll))
	if app.searching {
		meta = subtleStyle.Render("search: ") + app.search.View() + subtleStyle.Render(" | enter: apply | esc: cancel")
	}
	if app.loadingLogs {
		meta = app.spinner.View() + " resolving allocations | " + meta
	}
	if app.lastError != "" {
		meta = errorStyle.Render(app.lastError) + "\n" + meta
	}

	logs := app.logs.View()
	if app.showHelp {
		logs = app.logHelpView()
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		meta,
		logs,
	)
}

func (app app) logHelpView() string {
	width := app.logs.Width
	if width < 1 {
		width = app.width
	}
	if width < 1 {
		width = 1
	}
	height := app.logs.Height
	if height < 1 {
		height = 1
	}

	boxWidth := 62
	if width > 4 && boxWidth > width-4 {
		boxWidth = width - 4
	}
	if boxWidth < 24 {
		boxWidth = 24
	}

	box := helpBoxStyle.Width(boxWidth).Render(logHelpContent())
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func logHelpContent() string {
	return strings.Join([]string{
		titleStyle.Render("Logs"),
		"  esc              services",
		"  /                search logs",
		"  s                switch stdout/stderr",
		"  f                toggle follow",
		"  w                toggle wrap",
		"  J                toggle JSON highlight",
		"  up/down          scroll",
		"  pgup/pgdn        page",
		"  H/L              fast horizontal scroll",
		"  left/right       horizontal scroll",
		"  q                quit",
		"",
		titleStyle.Render("Search"),
		"  enter            apply",
		"  esc              cancel",
		"  up/ctrl+p        previous search",
		"  down/ctrl+n      next search",
		"",
		"  ?/h or esc       close help",
	}, "\n")
}

func (app app) loadServices() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		services, err := app.client.ListServices(ctx)
		return servicesLoadedMsg{services: services, err: err}
	}
}

func (app app) loadInstances(target serviceSummary, refresh bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		instances, err := app.client.TargetInstances(ctx, target)
		return instancesLoadedMsg{service: target.Name, instances: instances, refresh: refresh, err: err}
	}
}

func (app app) loadSearchHistory() tea.Cmd {
	if app.store == nil {
		return nil
	}
	return func() tea.Msg {
		queries, err := app.store.RecentSearches(app.searchHistoryContext(), searchHistoryLimit)
		if err != nil {
			return searchHistoryLoadedMsg{err: err}
		}
		return searchHistoryLoadedMsg{queries: queries}
	}
}

func (app app) saveSearchHistory(query string) tea.Cmd {
	if app.store == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	return func() tea.Msg {
		err := app.store.SaveSearch(query, app.searchHistoryContext())
		return searchHistorySavedMsg{err: err}
	}
}

func (app app) savePreferences() tea.Cmd {
	if app.store == nil {
		return nil
	}
	preferences := app.preferences()
	return func() tea.Msg {
		err := app.store.SavePreferences(preferences)
		return preferencesSavedMsg{err: err}
	}
}

func (app app) preferences() appPreferences {
	return appPreferences{
		logType:       app.config.logType,
		wrapLogs:      app.wrapLogs,
		follow:        app.follow,
		highlightJSON: app.highlightJSON,
	}
}

func (app app) searchHistoryContext() searchHistoryContext {
	return searchHistoryContext{
		service:   app.selectedService,
		namespace: app.config.namespace,
		region:    app.config.region,
	}
}

func (app *app) startStreams(service string, instances []serviceInstance) tea.Cmd {
	app.stopLogs()
	app.logGeneration++
	messages := make(chan tea.Msg, 256)
	app.logMessages = messages
	app.activeLogStreams = make(map[string]context.CancelFunc)
	app.reconcileLogStreams(service, instances)

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
			if !sendLogMessage(ctx, messages, logLineMsg{line: line, generation: app.logGeneration}) {
				return
			}
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				sendLogMessage(ctx, messages, streamErrorMsg{source: source, generation: app.logGeneration, err: err})
			}
			if ctx.Err() == nil {
				sendLogMessage(ctx, messages, logStreamStoppedMsg{source: source, generation: app.logGeneration})
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

func (app *app) reconcileLogStreams(service string, instances []serviceInstance) {
	app.selectedInstances = instances
	if app.logMessages == nil {
		return
	}
	if app.activeLogStreams == nil {
		app.activeLogStreams = make(map[string]context.CancelFunc)
	}

	desired := make(map[string]logSource)
	for _, source := range app.logSources(service, instances) {
		desired[logSourceKey(source)] = source
	}

	for key, cancel := range app.activeLogStreams {
		if _, ok := desired[key]; ok {
			continue
		}
		cancel()
		delete(app.activeLogStreams, key)
	}

	for key, source := range desired {
		if _, ok := app.activeLogStreams[key]; ok {
			continue
		}
		app.startLogStream(source)
	}
}

func (app *app) startLogStream(source logSource) {
	ctx, cancel := context.WithCancel(context.Background())
	app.activeLogStreams[logSourceKey(source)] = cancel
	go app.streamSource(ctx, source, app.logMessages)
}

func (app *app) removeLogStream(source logSource) {
	if app.activeLogStreams == nil {
		return
	}
	delete(app.activeLogStreams, logSourceKey(source))
}

func logSourceKey(source logSource) string {
	return source.Service + "\x00" + source.AllocID + "\x00" + source.Task + "\x00" + source.Stream
}

func (app *app) toggleLogStream() tea.Cmd {
	app.config.logType = nextLogStream(app.config.logType)
	app.clearLogBuffer()
	app.lineCount = 0
	app.lastError = ""
	app.logs.SetContent("")
	app.logs.GotoBottom()

	if app.selectedService == "" || len(app.selectedInstances) == 0 {
		return nil
	}
	return app.startStreams(app.selectedService, app.selectedInstances)
}

func nextLogStream(logType string) string {
	if logType == "stderr" {
		return "stdout"
	}
	return "stderr"
}

func (app *app) stopLogs() {
	for _, cancel := range app.activeLogStreams {
		cancel()
	}

	if app.logMessages != nil {
		select {
		case app.logMessages <- logsStoppedMsg{}:
		default:
		}
	}

	app.activeLogStreams = nil
	app.logMessages = nil
}

func (app *app) appendLine(line logLine) {
	prefix := fmt.Sprintf("[%s %s %s]", shortAlloc(line.Source.AllocID), line.Source.Task, line.Source.Stream)
	app.appendRawLine(logLevelPrefix(line.Text) + " " + logLineStyle.Render(prefix) + " " + line.Text)
}

func (app *app) appendSystemLine(line string) {
	app.appendRawLine(errorStyle.Render(line))
}

func (app *app) appendRawLine(line string) {
	app.logBuffer = append(app.logBuffer, line)
	droppedLines := app.dropOverflowingLogLines()
	app.appendToRenderCache(line, droppedLines)
	app.renderLogs()
	app.lineCount++
	if app.follow {
		app.logs.GotoBottom()
	}
}

func (app *app) dropOverflowingLogLines() []string {
	if app.config.maxLines < 1 {
		droppedLines := append([]string(nil), app.logBuffer...)
		app.logBuffer = nil
		return droppedLines
	}

	overflow := len(app.logBuffer) - app.config.maxLines
	if overflow <= 0 {
		return nil
	}

	droppedLines := append([]string(nil), app.logBuffer[:overflow]...)
	copy(app.logBuffer, app.logBuffer[overflow:])
	app.logBuffer = app.logBuffer[:app.config.maxLines]
	return droppedLines
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

func (app *app) renderedLogs() []string {
	query := app.currentSearchQuery()
	if app.renderCacheValid &&
		app.renderCacheQuery == query &&
		app.renderCacheJSON == app.highlightJSON &&
		app.renderCacheLen == len(app.logBuffer) {
		return app.renderCache
	}

	return app.rebuildRenderCache(query)
}

func (app *app) rebuildRenderCache(query string) []string {
	rendered := make([]string, 0, len(app.logBuffer))
	for _, line := range app.logBuffer {
		if app.lineMatchesSearch(line, query) {
			rendered = append(rendered, app.renderLogLine(line))
		}
	}

	app.renderCache = rendered
	app.renderCacheQuery = query
	app.renderCacheJSON = app.highlightJSON
	app.renderCacheLen = len(app.logBuffer)
	app.renderCacheValid = true
	return app.renderCache
}

func (app *app) appendToRenderCache(line string, droppedLines []string) {
	if !app.renderCacheValid {
		return
	}

	query := app.currentSearchQuery()
	if app.renderCacheQuery != query || app.renderCacheJSON != app.highlightJSON {
		app.invalidateRenderCache()
		return
	}

	for _, droppedLine := range droppedLines {
		if !app.lineMatchesSearch(droppedLine, query) {
			continue
		}
		if len(app.renderCache) == 0 {
			app.invalidateRenderCache()
			return
		}
		copy(app.renderCache, app.renderCache[1:])
		app.renderCache = app.renderCache[:len(app.renderCache)-1]
	}

	if len(app.logBuffer) > 0 && app.lineMatchesSearch(line, query) {
		app.renderCache = append(app.renderCache, app.renderLogLine(line))
	}
	app.renderCacheLen = len(app.logBuffer)
}

func (app *app) renderLogLine(line string) string {
	if app.highlightJSON {
		return highlightJSONLogLine(line)
	}
	return line
}

func (app *app) currentSearchQuery() string {
	return strings.TrimSpace(app.searchQuery)
}

func (app *app) setSearchQuery(query string) {
	previous := app.currentSearchQuery()
	app.searchQuery = query
	if previous != app.currentSearchQuery() {
		app.invalidateSearchCache()
		app.invalidateRenderCache()
	}
}

func (app *app) rememberSearch(query string) {
	app.searchHistory = prependSearchHistory(app.searchHistory, query)
	app.searchHistoryPos = -1
	app.searchDraft = ""
}

func prependSearchHistory(history []string, query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return history
	}

	merged := make([]string, 0, len(history)+1)
	merged = append(merged, query)
	for _, entry := range history {
		if entry == query {
			continue
		}
		merged = append(merged, entry)
		if len(merged) == searchHistoryLimit {
			break
		}
	}
	return merged
}

func mergeSearchHistory(current []string, loaded []string) []string {
	merged := make([]string, 0, len(current)+len(loaded))
	seen := make(map[string]struct{}, len(current)+len(loaded))
	for _, history := range [][]string{current, loaded} {
		for _, query := range history {
			query = strings.TrimSpace(query)
			if query == "" {
				continue
			}
			if _, ok := seen[query]; ok {
				continue
			}
			seen[query] = struct{}{}
			merged = append(merged, query)
			if len(merged) == searchHistoryLimit {
				return merged
			}
		}
	}
	return merged
}

func (app *app) previousSearch() {
	if len(app.searchHistory) == 0 {
		return
	}
	if app.searchHistoryPos < 0 {
		app.searchDraft = app.search.Value()
		app.searchHistoryPos = 0
	} else if app.searchHistoryPos < len(app.searchHistory)-1 {
		app.searchHistoryPos++
	}
	app.applySearchHistoryValue()
}

func (app *app) nextSearch() {
	if app.searchHistoryPos < 0 {
		return
	}
	if app.searchHistoryPos == 0 {
		app.searchHistoryPos = -1
		app.search.SetValue(app.searchDraft)
		app.setSearchQuery(app.searchDraft)
		return
	}
	app.searchHistoryPos--
	app.applySearchHistoryValue()
}

func (app *app) applySearchHistoryValue() {
	if app.searchHistoryPos < 0 || app.searchHistoryPos >= len(app.searchHistory) {
		return
	}
	query := app.searchHistory[app.searchHistoryPos]
	app.search.SetValue(query)
	app.setSearchQuery(query)
}

func (app *app) invalidateSearchCache() {
	app.searchCacheQuery = ""
	app.searchCacheExpr = nil
	app.searchCacheErr = nil
	app.searchCacheValid = false
}

func (app *app) invalidateRenderCache() {
	app.renderCache = nil
	app.renderCacheQuery = ""
	app.renderCacheJSON = false
	app.renderCacheLen = 0
	app.renderCacheValid = false
}

func (app *app) clearLogBuffer() {
	app.logBuffer = nil
	app.invalidateRenderCache()
}

func (app *app) lineMatchesSearch(line string, query string) bool {
	if query == "" {
		return true
	}

	expression, err := app.cachedSearchExpression(query)
	return searchExpressionMatches(line, query, expression, err)
}

func (app *app) cachedSearchExpression(query string) (searchExpression, error) {
	if app.searchCacheValid && app.searchCacheQuery == query {
		return app.searchCacheExpr, app.searchCacheErr
	}

	expression, err := parseLogSearch(query)
	app.searchCacheQuery = query
	app.searchCacheExpr = expression
	app.searchCacheErr = err
	app.searchCacheValid = true
	return expression, err
}

func (app *app) filteredLogs() []string {
	query := app.currentSearchQuery()
	if query == "" {
		return append([]string(nil), app.logBuffer...)
	}

	logs := make([]string, 0, len(app.logBuffer))
	for _, line := range app.logBuffer {
		if app.lineMatchesSearch(line, query) {
			logs = append(logs, line)
		}
	}
	return logs
}

func logMatchesSearch(line string, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return true
	}
	expression, err := parseLogSearch(query)
	return searchExpressionMatches(line, query, expression, err)
}

func searchExpressionMatches(line string, query string, expression searchExpression, err error) bool {
	if err != nil {
		return valueMatchesSearch(line, query)
	}
	return expression.matches(line)
}

type searchExpression interface {
	matches(line string) bool
}

type textSearchExpression struct {
	query string
}

func (expression textSearchExpression) matches(line string) bool {
	return valueMatchesSearch(line, expression.query)
}

type fieldSearchExpression struct {
	field string
	value string
}

func (expression fieldSearchExpression) matches(line string) bool {
	return logJSONFieldMatches(line, expression.field, expression.value)
}

type notSearchExpression struct {
	expression searchExpression
}

func (expression notSearchExpression) matches(line string) bool {
	return !expression.expression.matches(line)
}

type andSearchExpression struct {
	left  searchExpression
	right searchExpression
}

func (expression andSearchExpression) matches(line string) bool {
	return expression.left.matches(line) && expression.right.matches(line)
}

type orSearchExpression struct {
	left  searchExpression
	right searchExpression
}

func (expression orSearchExpression) matches(line string) bool {
	return expression.left.matches(line) || expression.right.matches(line)
}

type searchTokenKind int

const (
	searchTokenText searchTokenKind = iota
	searchTokenAnd
	searchTokenOr
	searchTokenNot
	searchTokenLeftParen
	searchTokenRightParen
)

type searchToken struct {
	kind  searchTokenKind
	value string
}

type searchParser struct {
	tokens []searchToken
	offset int
}

func parseLogSearch(query string) (searchExpression, error) {
	tokens, err := tokenizeLogSearch(query)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return textSearchExpression{}, nil
	}

	parser := searchParser{tokens: tokens}
	expression, err := parser.parseOr()
	if err != nil {
		return nil, err
	}
	if parser.hasNext() {
		return nil, fmt.Errorf("unexpected token %q", parser.peek().value)
	}
	return expression, nil
}

func tokenizeLogSearch(query string) ([]searchToken, error) {
	var tokens []searchToken
	for offset := 0; offset < len(query); {
		switch {
		case query[offset] == ' ' || query[offset] == '\t' || query[offset] == '\n' || query[offset] == '\r':
			offset++
		case query[offset] == '(':
			tokens = append(tokens, searchToken{kind: searchTokenLeftParen, value: "("})
			offset++
		case query[offset] == ')':
			tokens = append(tokens, searchToken{kind: searchTokenRightParen, value: ")"})
			offset++
		case query[offset] == '-':
			tokens = append(tokens, searchToken{kind: searchTokenNot, value: "-"})
			offset++
		case query[offset] == '"':
			value, next, err := scanQuotedSearchValue(query, offset)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, searchToken{kind: searchTokenText, value: value})
			offset = next
		default:
			value, next, err := scanBareSearchValue(query, offset)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, searchOperatorToken(value))
			offset = next
		}
	}
	return tokens, nil
}

func scanQuotedSearchValue(query string, offset int) (string, int, error) {
	var value strings.Builder
	for offset++; offset < len(query); offset++ {
		if query[offset] == '"' {
			return value.String(), offset + 1, nil
		}
		value.WriteByte(query[offset])
	}
	return "", offset, fmt.Errorf("unterminated quoted search term")
}

func scanBareSearchValue(query string, offset int) (string, int, error) {
	var value strings.Builder
	for offset < len(query) {
		switch query[offset] {
		case ' ', '\t', '\n', '\r', '(', ')':
			return value.String(), offset, nil
		case '"':
			quoted, next, err := scanQuotedSearchValue(query, offset)
			if err != nil {
				return "", offset, err
			}
			value.WriteString(quoted)
			offset = next
		case '/':
			pattern, next, err := scanSlashSearchValue(query, offset)
			if err != nil {
				return "", offset, err
			}
			value.WriteString(pattern)
			offset = next
		default:
			value.WriteByte(query[offset])
			offset++
		}
	}
	return value.String(), offset, nil
}

func scanSlashSearchValue(query string, offset int) (string, int, error) {
	start := offset
	for offset++; offset < len(query); offset++ {
		if query[offset] == '/' {
			return query[start : offset+1], offset + 1, nil
		}
	}
	return "", offset, fmt.Errorf("unterminated slash search term")
}

func searchOperatorToken(value string) searchToken {
	switch value {
	case "AND":
		return searchToken{kind: searchTokenAnd, value: value}
	case "OR":
		return searchToken{kind: searchTokenOr, value: value}
	case "NOT":
		return searchToken{kind: searchTokenNot, value: value}
	default:
		return searchToken{kind: searchTokenText, value: value}
	}
}

func (parser *searchParser) parseOr() (searchExpression, error) {
	left, err := parser.parseAnd()
	if err != nil {
		return nil, err
	}

	for parser.match(searchTokenOr) {
		right, err := parser.parseAnd()
		if err != nil {
			return nil, err
		}
		left = orSearchExpression{left: left, right: right}
	}
	return left, nil
}

func (parser *searchParser) parseAnd() (searchExpression, error) {
	left, err := parser.parseUnary()
	if err != nil {
		return nil, err
	}

	for {
		if parser.match(searchTokenAnd) {
			right, err := parser.parseUnary()
			if err != nil {
				return nil, err
			}
			left = andSearchExpression{left: left, right: right}
			continue
		}
		if !parser.nextStartsExpression() {
			return left, nil
		}
		right, err := parser.parseUnary()
		if err != nil {
			return nil, err
		}
		left = andSearchExpression{left: left, right: right}
	}
}

func (parser *searchParser) parseUnary() (searchExpression, error) {
	if parser.match(searchTokenNot) {
		expression, err := parser.parseUnary()
		if err != nil {
			return nil, err
		}
		return notSearchExpression{expression: expression}, nil
	}
	return parser.parsePrimary()
}

func (parser *searchParser) parsePrimary() (searchExpression, error) {
	token, ok := parser.advance()
	if !ok {
		return nil, fmt.Errorf("missing search expression")
	}

	switch token.kind {
	case searchTokenText:
		field, value, ok := parseJSONFieldSearch(token.value)
		if ok {
			return fieldSearchExpression{field: field, value: value}, nil
		}
		return textSearchExpression{query: token.value}, nil
	case searchTokenLeftParen:
		expression, err := parser.parseOr()
		if err != nil {
			return nil, err
		}
		if !parser.match(searchTokenRightParen) {
			return nil, fmt.Errorf("missing closing parenthesis")
		}
		return expression, nil
	default:
		return nil, fmt.Errorf("unexpected token %q", token.value)
	}
}

func (parser *searchParser) hasNext() bool {
	return parser.offset < len(parser.tokens)
}

func (parser *searchParser) peek() searchToken {
	if !parser.hasNext() {
		return searchToken{}
	}
	return parser.tokens[parser.offset]
}

func (parser *searchParser) advance() (searchToken, bool) {
	if !parser.hasNext() {
		return searchToken{}, false
	}
	token := parser.tokens[parser.offset]
	parser.offset++
	return token, true
}

func (parser *searchParser) match(kind searchTokenKind) bool {
	if !parser.hasNext() || parser.peek().kind != kind {
		return false
	}
	parser.offset++
	return true
}

func (parser *searchParser) nextStartsExpression() bool {
	if !parser.hasNext() {
		return false
	}
	switch parser.peek().kind {
	case searchTokenText, searchTokenNot, searchTokenLeftParen:
		return true
	default:
		return false
	}
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
	if object, ok := payload.(map[string]any); ok {
		if value, ok := lookupJSONFieldPart(object, field); ok {
			return value, true
		}
	}

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

func logLevelPrefix(line string) string {
	level, ok := logLevel(line)
	if !ok {
		return subtleStyle.Render("     ")
	}

	label := fmt.Sprintf("%-5s", level)
	switch level {
	case "DEBUG":
		return debugLevelStyle.Render(label)
	case "INFO":
		return infoLevelStyle.Render(label)
	case "WARN":
		return warnLevelStyle.Render(label)
	case "ERROR", "CRIT":
		return errorLevelStyle.Render(label)
	default:
		return subtleStyle.Render(label)
	}
}

func logLevel(line string) (string, bool) {
	plain := ansi.Strip(line)
	jsonStart, jsonEnd := jsonRange(plain)
	if jsonStart < 0 {
		return "", false
	}

	var payload any
	if err := json.Unmarshal([]byte(plain[jsonStart:jsonEnd]), &payload); err != nil {
		return "", false
	}

	value, ok := jsonFieldValue(payload, "level")
	if !ok {
		return "", false
	}
	level := strings.ToUpper(strings.TrimSpace(jsonValueString(value)))
	switch level {
	case "DEBUG", "INFO", "WARN", "ERROR", "CRIT":
		return level, true
	case "WARNING":
		return "WARN", true
	case "CRITICAL", "FATAL":
		return "CRIT", true
	default:
		return "", false
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
	if start+1 >= len(value) {
		return len(value) - 1
	}

	index := start + 1
	if value[index] == '[' {
		index++
	}
	for ; index < len(value); index++ {
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
	helpBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(1, 2)

	serviceSelectedMarkerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	serviceNameStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	serviceSelectedNameStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	serviceCountStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	serviceSourceStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	serviceTypeStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	serviceProviderStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))
	serviceTagsStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	serviceSeparatorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	serviceStatusGoodStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	serviceStatusWarnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	serviceStatusBadStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	serviceStatusDefaultStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))

	debugLevelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	infoLevelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnLevelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	errorLevelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	jsonKeyStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	jsonStringStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	jsonNumberStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	jsonBoolStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("219"))
	jsonNullStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	jsonPunctuationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
)
