package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Arctize/ctop/internal/docker"
)

type tab int

const (
	tabContainers tab = iota
	tabImages
	tabNetworks
	tabVolumes
	tabSystem
	numTabs
)

var tabNames = [numTabs]string{"Containers", "Images", "Networks", "Volumes", "System"}

type mode int

const (
	modeList mode = iota
	modeLogs
	modeInspect
	modeHelp
)

// Config holds startup options for the UI.
type Config struct {
	Interval time.Duration
	ShowAll  bool
	Group    bool
	Tail     int
}

type statusKind int

const (
	stInfo statusKind = iota
	stOK
	stErr
	stBusy
)

type confirmPrompt struct {
	prompt string
	run    func() tea.Cmd
}

// Model is the root Bubble Tea model.
type Model struct {
	cli *docker.Client
	cfg Config

	w, h int
	tab  tab
	mode mode

	lists     [numTabs]listState
	rows      []row
	filter    textinput.Model
	filtering bool
	confirm   *confirmPrompt

	status     string
	statusKind statusKind
	statusAt   time.Time

	info    *docker.Info
	version *docker.Version
	connErr error

	containers []docker.Container
	cstats     map[string]*cstat
	prev       map[string]*docker.Stats
	fetching   bool
	loaded     bool

	images     []docker.Image
	networks   []docker.Network
	volumes    []docker.Volume
	volSizes   map[string]int64
	df         *docker.DiskUsage
	dfLoading  bool
	resLoading [numTabs]bool

	showAll   bool
	group     bool
	collapsed map[string]bool

	logs    *logView
	logGen  int
	inspect *pager
}

// New creates the UI model.
func New(cli *docker.Client, cfg Config) *Model {
	if cfg.Interval <= 0 {
		cfg.Interval = 2 * time.Second
	}
	if cfg.Tail <= 0 {
		cfg.Tail = 1000
	}
	fi := textinput.New()
	fi.Prompt = "/"
	fi.Placeholder = "filter: name, image, project, state…"
	fi.CharLimit = 200
	m := &Model{
		cli:       cli,
		cfg:       cfg,
		filter:    fi,
		cstats:    map[string]*cstat{},
		prev:      map[string]*docker.Stats{},
		volSizes:  map[string]int64{},
		showAll:   cfg.ShowAll,
		group:     cfg.Group,
		collapsed: map[string]bool{},
	}
	for i := range m.lists {
		m.lists[i].sortBy = "name"
	}
	return m
}

// ---- messages

type tickMsg time.Time

type containersMsg struct {
	list  []docker.Container
	stats map[string]*docker.Stats
	at    time.Time
	err   error
}

type resourcesMsg struct {
	tab      tab
	images   []docker.Image
	networks []docker.Network
	volumes  []docker.Volume
	err      error
}

type infoMsg struct {
	info    *docker.Info
	version *docker.Version
	err     error
}

type dfMsg struct {
	df  *docker.DiskUsage
	vol bool
	err error
}

type actionMsg struct {
	text    string
	err     error
	refresh tab
}

type inspectMsg struct {
	title string
	lines []string
	err   error
}

type execDoneMsg struct {
	text string
	err  error
}

// ---- commands

func (m *Model) tick() tea.Cmd {
	return tea.Tick(m.cfg.Interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) fetchInfo() tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		info, err := cli.Info(ctx)
		if err != nil {
			return infoMsg{err: err}
		}
		v, err := cli.Version(ctx)
		return infoMsg{info: info, version: v, err: err}
	}
}

func (m *Model) fetchContainers() tea.Cmd {
	m.fetching = true
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		list, err := cli.ContainerList(ctx, true)
		if err != nil {
			return containersMsg{err: err}
		}
		stats := map[string]*docker.Stats{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, c := range list {
			if c.State != "running" {
				continue
			}
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				s, err := cli.ContainerStats(ctx, id)
				if err == nil {
					mu.Lock()
					stats[id] = s
					mu.Unlock()
				}
			}(c.ID)
		}
		wg.Wait()
		return containersMsg{list: list, stats: stats, at: time.Now()}
	}
}

func (m *Model) fetchResources(t tab) tea.Cmd {
	if m.resLoading[t] {
		return nil
	}
	m.resLoading[t] = true
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		msg := resourcesMsg{tab: t}
		switch t {
		case tabImages:
			msg.images, msg.err = cli.ImageList(ctx)
		case tabNetworks:
			msg.networks, msg.err = cli.NetworkList(ctx)
		case tabVolumes:
			msg.volumes, msg.err = cli.VolumeList(ctx)
		}
		return msg
	}
}

func (m *Model) fetchDF(volumesOnly bool) tea.Cmd {
	if m.dfLoading {
		return nil
	}
	m.dfLoading = true
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		typ := ""
		if volumesOnly {
			typ = "volume"
		}
		df, err := cli.DiskUsage(ctx, typ)
		return dfMsg{df: df, vol: volumesOnly, err: err}
	}
}

// refreshTab returns the commands that reload the data shown on tab t.
func (m *Model) refreshTab(t tab, full bool) tea.Cmd {
	var cmds []tea.Cmd
	if !m.fetching {
		cmds = append(cmds, m.fetchContainers())
	}
	switch t {
	case tabImages, tabNetworks:
		cmds = append(cmds, m.fetchResources(t))
	case tabVolumes:
		cmds = append(cmds, m.fetchResources(t))
		if full {
			cmds = append(cmds, m.fetchDF(true))
		}
	case tabSystem:
		if full {
			cmds = append(cmds, m.fetchInfo(), m.fetchDF(false))
		}
	}
	return tea.Batch(cmds...)
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.fetchInfo(), m.fetchContainers(), m.tick())
}

func (m *Model) setStatus(kind statusKind, format string, a ...any) {
	m.status = fmt.Sprintf(format, a...)
	m.statusKind = kind
	m.statusAt = time.Now()
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.filter.Width = m.w - 4
		m.rebuild()
		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{m.tick()}
		if !m.fetching {
			cmds = append(cmds, m.fetchContainers())
		}
		if m.mode == modeList && (m.tab == tabImages || m.tab == tabNetworks || m.tab == tabVolumes) {
			cmds = append(cmds, m.fetchResources(m.tab))
		}
		return m, tea.Batch(cmds...)

	case containersMsg:
		m.fetching = false
		if msg.err != nil {
			m.connErr = msg.err
			return m, nil
		}
		m.connErr = nil
		m.loaded = true
		m.containers = msg.list
		m.updateStats(msg.stats, msg.at)
		m.rebuild()
		return m, nil

	case resourcesMsg:
		m.resLoading[msg.tab] = false
		if msg.err != nil {
			m.setStatus(stErr, "%v", msg.err)
			return m, nil
		}
		switch msg.tab {
		case tabImages:
			m.images = msg.images
		case tabNetworks:
			m.networks = msg.networks
		case tabVolumes:
			m.volumes = msg.volumes
		}
		m.rebuild()
		return m, nil

	case infoMsg:
		if msg.err != nil {
			m.connErr = msg.err
			return m, nil
		}
		m.info, m.version = msg.info, msg.version
		return m, nil

	case dfMsg:
		m.dfLoading = false
		if msg.err != nil {
			m.setStatus(stErr, "disk usage: %v", msg.err)
			return m, nil
		}
		for _, v := range msg.df.Volumes {
			if v.UsageData != nil {
				m.volSizes[v.Name] = v.UsageData.Size
			}
		}
		if !msg.vol {
			m.df = msg.df
		}
		m.rebuild()
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.setStatus(stErr, "%v", msg.err)
		} else {
			m.setStatus(stOK, "%s", msg.text)
		}
		return m, m.refreshTab(msg.refresh, msg.refresh == tabVolumes || msg.refresh == tabSystem)

	case inspectMsg:
		if msg.err != nil {
			m.setStatus(stErr, "%v", msg.err)
			m.mode = modeList
			return m, nil
		}
		if m.mode == modeInspect && m.inspect != nil {
			m.inspect.title = msg.title
			m.inspect.lines = msg.lines
		}
		return m, nil

	case execDoneMsg:
		if msg.err != nil {
			m.setStatus(stErr, "%v", msg.err)
		} else if msg.text != "" {
			m.setStatus(stInfo, "%s", msg.text)
		}
		return m, tea.Batch(m.fetchContainers(), tea.ClearScreen)

	case logBatchMsg:
		if m.logs == nil || msg.gen != m.logs.gen {
			return m, nil
		}
		m.logs.add(msg.lines)
		return m, waitLog(msg.gen, m.logs.ch)

	case logEndMsg:
		if m.logs != nil && msg.gen == m.logs.gen {
			m.logs.ended = true
		}
		return m, nil

	case tea.MouseMsg:
		return m, m.handleMouse(msg)

	case tea.KeyMsg:
		// Several keys arriving in one read (fast typing, key repeat) are
		// delivered as one multi-rune message; handle them one by one.
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste {
			var cmds []tea.Cmd
			for _, r := range msg.Runes {
				cmds = append(cmds, m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: msg.Alt}))
			}
			return m, tea.Batch(cmds...)
		}
		return m, m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Action != tea.MouseActionPress {
		return nil
	}
	delta := 0
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		delta = -3
	case tea.MouseButtonWheelDown:
		delta = 3
	}
	switch m.mode {
	case modeLogs:
		if delta != 0 {
			m.logs.scroll(delta, m.bodyHeight())
		}
	case modeInspect:
		if delta != 0 {
			m.inspect.scroll(delta, m.bodyHeight())
		}
	case modeList:
		if m.tab == tabSystem {
			return nil
		}
		st := &m.lists[m.tab]
		if delta != 0 {
			st.move(m.rows, delta, m.tableHeight())
			return nil
		}
		if msg.Button == tea.MouseButtonLeft {
			// header (3 lines) + column header (1 line)
			idx := st.offset + msg.Y - 4
			if msg.Y >= 4 && idx >= 0 && idx < len(m.rows) {
				st.move(m.rows, idx-st.cursor, m.tableHeight())
			} else if msg.Y == 2 {
				// click on tab bar
				x := 1
				for t := tab(0); t < numTabs; t++ {
					wdt := len(tabNames[t]) + 4
					if msg.X >= x && msg.X < x+wdt {
						return m.switchTab(t)
					}
					x += wdt
				}
			}
		}
	}
	return nil
}

func (m *Model) switchTab(t tab) tea.Cmd {
	if t == m.tab {
		return nil
	}
	m.tab = t
	m.filter.SetValue("")
	m.filtering = false
	m.filter.Blur()
	m.rebuild()
	return m.refreshTab(t, true)
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		m.closeLogs()
		return tea.Quit
	}
	switch m.mode {
	case modeHelp:
		m.mode = modeList
		return nil
	case modeLogs:
		return m.logsKey(msg)
	case modeInspect:
		return m.inspectKey(msg)
	}

	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if key == "y" || key == "Y" {
			return c.run()
		}
		m.setStatus(stInfo, "cancelled")
		return nil
	}

	if m.filtering {
		switch key {
		case "enter":
			m.filtering = false
			m.filter.Blur()
			return nil
		case "esc":
			m.filtering = false
			m.filter.Blur()
			m.filter.SetValue("")
			m.rebuild()
			return nil
		case "up", "down", "pgup", "pgdown":
			// allow navigating while typing
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.rebuild()
			return cmd
		}
	}

	st := &m.lists[m.tab]
	th := m.tableHeight()
	switch key {
	case "q":
		m.closeLogs()
		return tea.Quit
	case "?", "f1":
		m.mode = modeHelp
		return nil
	case "1", "2", "3", "4", "5":
		return m.switchTab(tab(key[0] - '1'))
	case "tab", "right":
		return m.switchTab((m.tab + 1) % numTabs)
	case "shift+tab", "left":
		return m.switchTab((m.tab + numTabs - 1) % numTabs)
	case "/":
		if m.tab != tabSystem {
			m.filtering = true
			return m.filter.Focus()
		}
	case "esc":
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.rebuild()
		}
		return nil
	case "ctrl+r", "f5":
		m.setStatus(stInfo, "refreshing…")
		return m.refreshTab(m.tab, true)
	case "up", "k":
		st.move(m.rows, -1, th)
	case "down", "j":
		st.move(m.rows, 1, th)
	case "pgup", "ctrl+b":
		st.move(m.rows, -th, th)
	case "pgdown", "ctrl+f":
		st.move(m.rows, th, th)
	case "home":
		st.move(m.rows, -len(m.rows), th)
	case "end", "G":
		st.move(m.rows, len(m.rows), th)
	case ">", "f6":
		m.cycleSort(1)
	case "<":
		m.cycleSort(-1)
	case "i", "I":
		st.desc = !st.desc
		m.rebuild()
	default:
		switch m.tab {
		case tabContainers:
			return m.containerKey(key)
		case tabImages, tabNetworks, tabVolumes:
			return m.resourceKey(key)
		}
	}
	return nil
}

func (m *Model) cycleSort(dir int) {
	cols := m.columns()
	var keys []string
	for _, c := range cols {
		if c.sort != "" {
			keys = append(keys, c.sort)
		}
	}
	if len(keys) == 0 {
		return
	}
	st := &m.lists[m.tab]
	idx := 0
	for i, k := range keys {
		if k == st.sortBy {
			idx = i
		}
	}
	idx = (idx + dir + len(keys)) % len(keys)
	st.sortBy = keys[idx]
	// numeric columns default to descending
	st.desc = st.sortBy != "name" && st.sortBy != "image" && st.sortBy != "state" && st.sortBy != "project" && st.sortBy != "driver"
	m.rebuild()
	m.setStatus(stInfo, "sort: %s", st.sortBy)
}

func (m *Model) setSort(key string, desc bool) {
	st := &m.lists[m.tab]
	if st.sortBy == key {
		st.desc = !st.desc
	} else {
		st.sortBy, st.desc = key, desc
	}
	m.rebuild()
	m.setStatus(stInfo, "sort: %s", key)
}

func (m *Model) ask(prompt string, run func() tea.Cmd) {
	m.confirm = &confirmPrompt{prompt: prompt, run: run}
}

// runAll runs fn for each id concurrently and reports a combined result.
func (m *Model) runAll(verb, done string, ids, names []string, refresh tab, fn func(ctx context.Context, id string) error) tea.Cmd {
	what := describe(names)
	if len(names) > 1 && refresh == tabContainers {
		what = fmt.Sprintf("%d containers", len(names))
	}
	m.setStatus(stBusy, "%s %s…", verb, what)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		var mu sync.Mutex
		var errs []string
		var wg sync.WaitGroup
		for i, id := range ids {
			wg.Add(1)
			go func(i int, id string) {
				defer wg.Done()
				if err := fn(ctx, id); err != nil {
					mu.Lock()
					errs = append(errs, fmt.Sprintf("%s: %v", names[i], err))
					mu.Unlock()
				}
			}(i, id)
		}
		wg.Wait()
		if len(errs) > 0 {
			return actionMsg{err: errors.New(strings.Join(errs, "; ")), refresh: refresh}
		}
		return actionMsg{text: done + " " + what, refresh: refresh}
	}
}

func describe(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return fmt.Sprintf("%d items", len(names))
}

// ---- layout helpers

const headerLines = 3

func (m *Model) bodyHeight() int {
	h := m.h - headerLines - 2 // status + footer
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) tableHeight() int {
	h := m.bodyHeight() - 1 // column header
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) columns() []column {
	switch m.tab {
	case tabContainers:
		return containerColumns
	case tabImages:
		return imageColumns
	case tabNetworks:
		return networkColumns
	case tabVolumes:
		return volumeColumns
	}
	return nil
}

// rebuild recomputes the rows of the active tab.
func (m *Model) rebuild() {
	switch m.tab {
	case tabContainers:
		m.rows = m.containerRows()
	case tabImages:
		m.rows = m.imageRows()
	case tabNetworks:
		m.rows = m.networkRows()
	case tabVolumes:
		m.rows = m.volumeRows()
	default:
		m.rows = nil
	}
	m.lists[m.tab].sync(m.rows, m.tableHeight())
}

func (m *Model) selected() *row {
	st := &m.lists[m.tab]
	if st.cursor >= 0 && st.cursor < len(m.rows) {
		return &m.rows[st.cursor]
	}
	return nil
}

func (m *Model) filterTerms() []string {
	return strings.Fields(strings.ToLower(m.filter.Value()))
}

func matchTerms(terms []string, hay ...string) bool {
	if len(terms) == 0 {
		return true
	}
	h := strings.ToLower(strings.Join(hay, " "))
	for _, t := range terms {
		if !strings.Contains(h, t) {
			return false
		}
	}
	return true
}

// ---- view

// View implements tea.Model.
func (m *Model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.viewHeader())
	b.WriteByte('\n')

	bh := m.bodyHeight()
	var body []string
	switch m.mode {
	case modeHelp:
		body = m.viewHelp(bh)
	case modeLogs:
		body = m.logs.view(m.w, bh)
	case modeInspect:
		body = m.inspect.view(m.w, bh)
	default:
		switch m.tab {
		case tabSystem:
			body = m.viewSystem(bh)
		default:
			body = m.viewTable(bh)
		}
	}
	for i := 0; i < bh; i++ {
		if i < len(body) {
			b.WriteString(fit(body[i], m.w, false))
		} else {
			b.WriteString(strings.Repeat(" ", m.w))
		}
		b.WriteByte('\n')
	}
	b.WriteString(fit(m.viewStatus(), m.w, false))
	b.WriteByte('\n')
	b.WriteString(fit(m.viewFooter(), m.w, false))
	return b.String()
}

func (m *Model) viewTable(height int) []string {
	cols := m.columns()
	l := layoutColumns(cols, m.w)
	st := &m.lists[m.tab]
	lines := []string{renderHeader(cols, l, st, m.w)}
	th := height - 1
	if len(m.rows) == 0 {
		msg := "nothing here"
		switch {
		case !m.loaded || m.resLoading[m.tab]:
			msg = "loading…"
		case m.filter.Value() != "":
			msg = "no matches for filter"
		case m.tab == tabContainers && !m.showAll:
			msg = "no running containers (press . to show all)"
		}
		lines = append(lines, "", sSubtle.Render("  "+msg))
		return lines
	}
	end := st.offset + th
	if end > len(m.rows) {
		end = len(m.rows)
	}
	for i := st.offset; i < end; i++ {
		lines = append(lines, renderRow(m.rows[i], cols, l, i == st.cursor, m.w))
	}
	return lines
}

func (m *Model) viewHeader() string {
	w := m.w
	// line 1: identity
	left := sBadge.Render("ctop") + " " + sAccent.Bold(true).Render(m.cli.Host)
	if m.info != nil {
		left += sSubtle.Render(" · ") + m.info.Name
		if m.version != nil {
			left += sSubtle.Render(" · docker ") + m.version.Version
		}
		left += sSubtle.Render(fmt.Sprintf(" · %d cpu · %s", m.info.NCPU, humanBytes(float64(m.info.MemTotal))))
	}
	right := sSubtle.Render(time.Now().Format("15:04:05"))
	if m.connErr != nil {
		right = sErr.Render("● disconnected ") + right
	} else if m.fetching {
		right = sSubtle.Render("⟳ ") + right
	}
	line1 := joinLR(" "+left, right+" ", w)

	// line 2: meters & counts
	var cpuSum, memSum float64
	for _, s := range m.cstats {
		cpuSum += s.cpu
		memSum += float64(s.mem)
	}
	ncpu, memTotal := 1, 1.0
	if m.info != nil {
		if m.info.NCPU > 0 {
			ncpu = m.info.NCPU
		}
		if m.info.MemTotal > 0 {
			memTotal = float64(m.info.MemTotal)
		}
	}
	bw := 20
	if w < 110 {
		bw = 10
	}
	if w < 80 {
		bw = 5
	}
	cpuFrac := cpuSum / float64(ncpu*100)
	line2 := fmt.Sprintf(" %s %s %s   %s %s %s",
		sBold.Render("CPU"), bar(cpuFrac, bw), fit(fmt.Sprintf("%.1f%%", cpuFrac*100), 6, true),
		sBold.Render("MEM"), bar(memSum/memTotal, bw), humanBytes(memSum)+sSubtle.Render("/"+humanBytes(memTotal)))
	var running, paused, stopped int
	for i := range m.containers {
		switch m.containers[i].State {
		case "running", "restarting":
			running++
		case "paused":
			paused++
		default:
			stopped++
		}
	}
	counts := sGreen.Render(fmt.Sprintf("● %d running", running))
	if paused > 0 {
		counts += "  " + sYellow.Render(fmt.Sprintf("◐ %d paused", paused))
	}
	counts += "  " + sSubtle.Render(fmt.Sprintf("○ %d stopped", stopped))
	line2 = joinLR(line2, counts+" ", w)

	// line 3: tabs + view flags
	var tabs strings.Builder
	tabs.WriteString(" ")
	for t := tab(0); t < numTabs; t++ {
		label := fmt.Sprintf("%d %s", t+1, tabNames[t])
		if t == m.tab {
			tabs.WriteString(" " + sTabActive.Render(label) + "  ")
		} else {
			tabs.WriteString(" " + sTabIdle.Render(label) + "  ")
		}
	}
	var flags []string
	switch m.mode {
	case modeLogs:
		flags = append(flags, m.logs.flags())
	case modeInspect:
	default:
		if m.tab == tabContainers {
			if m.showAll {
				flags = append(flags, "all")
			} else {
				flags = append(flags, "running")
			}
			if m.group {
				flags = append(flags, "grouped")
			}
		}
		if m.tab != tabSystem {
			if f := m.filter.Value(); f != "" && !m.filtering {
				flags = append(flags, sYellow.Render("filter: "+f))
			}
			flags = append(flags, fmt.Sprintf("%d rows", len(m.rows)))
		}
	}
	line3 := joinLR(tabs.String(), sSubtle.Render(strings.Join(flags, " · "))+" ", w)
	return line1 + "\n" + line2 + "\n" + line3
}

func (m *Model) viewStatus() string {
	if m.confirm != nil {
		return " " + sWarn.Render(m.confirm.prompt) + sSubtle.Render(" [y/N]")
	}
	if m.mode == modeLogs && m.logs.filtering {
		return " " + m.logs.input.View()
	}
	if m.mode == modeInspect && m.inspect.searching {
		return " " + m.inspect.input.View()
	}
	if m.filtering {
		return " " + m.filter.View()
	}
	if m.connErr != nil {
		return " " + sErr.Render("error: ") + ansi.Strip(m.connErr.Error())
	}
	if m.status == "" || (m.statusKind != stBusy && time.Since(m.statusAt) > 6*time.Second) {
		return ""
	}
	switch m.statusKind {
	case stErr:
		return " " + sErr.Render("✗ ") + m.status
	case stOK:
		return " " + sOK.Render("✓ ") + m.status
	case stBusy:
		return " " + sYellow.Render("… ") + m.status
	}
	return " " + sSubtle.Render(m.status)
}

func hints(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		b.WriteString(" " + sKey.Render(pairs[i]) + " " + sSubtle.Render(pairs[i+1]) + " ")
	}
	return b.String()
}

func (m *Model) viewFooter() string {
	switch m.mode {
	case modeHelp:
		return hints("any key", "close")
	case modeLogs:
		return hints("esc", "back", "f", "follow", "w", "wrap", "t", "timestamps", "/", "filter", "←→", "scroll", "g/G", "top/bottom", "c", "clear")
	case modeInspect:
		return hints("esc", "back", "↑↓", "scroll", "/", "search", "n/N", "next/prev", "←→", "pan")
	}
	switch m.tab {
	case tabContainers:
		return hints("⏎", "inspect", "l", "logs", "s", "shell", "a", "attach", "u", "start", "x", "stop", "r", "restart",
			"p", "pause", "^d", "delete", "/", "filter", "<>", "sort", "g", "group", ".", "all", "?", "help")
	case tabImages, tabNetworks, tabVolumes:
		return hints("⏎", "inspect", "^d", "delete", "X", "prune", "/", "filter", "<>", "sort", "i", "invert", "^r", "refresh", "?", "help", "q", "quit")
	}
	return hints("^r", "refresh", "tab", "switch view", "?", "help", "q", "quit")
}
