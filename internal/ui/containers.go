package ui

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Arctize/ctop/internal/docker"
)

var containerColumns = []column{
	{title: "NAME", width: 22, flex: true, sort: "name"},
	{title: "STATE", width: 13, sort: "state"},
	{title: "CPU", width: 12, prio: 1, sort: "cpu"},
	{title: "MEM", width: 12, prio: 1, sort: "mem"},
	{title: "NET ↓/↑", width: 13, prio: 4, sort: "net"},
	{title: "PIDS", width: 5, right: true, prio: 6, sort: "pids"},
	{title: "IMAGE", width: 26, prio: 3, sort: "image", grow: true},
	{title: "PORTS", width: 16, prio: 5},
	{title: "STATUS", width: 14, prio: 2, sort: "created"},
}

// cstat holds derived per-container metrics.
type cstat struct {
	cpu      float64 // percent, 100 = one core
	hasCPU   bool
	mem      uint64
	memLimit uint64
	rx, tx   float64 // bytes/s
	pids     uint64
}

func (m *Model) updateStats(samples map[string]*docker.Stats, at time.Time) {
	ncpu := 1
	if m.info != nil && m.info.NCPU > 0 {
		ncpu = m.info.NCPU
	}
	next := map[string]*cstat{}
	for id, cur := range samples {
		cs := &cstat{
			mem:      cur.MemUsed(),
			memLimit: cur.MemoryStats.Limit,
			pids:     cur.PidsStats.Current,
		}
		old := m.cstats[id]
		prev := m.prev[id]
		if prev != nil {
			dt := cur.Read.Sub(prev.Read).Seconds()
			if dt < 0.5 { // too close to the previous sample to be meaningful
				if old != nil {
					next[id] = old
				}
				continue
			}
			cpuD := float64(cur.CPUStats.CPUUsage.TotalUsage) - float64(prev.CPUStats.CPUUsage.TotalUsage)
			sysD := float64(cur.CPUStats.SystemUsage) - float64(prev.CPUStats.SystemUsage)
			online := int(cur.CPUStats.OnlineCPUs)
			if online == 0 {
				online = len(cur.CPUStats.CPUUsage.PercpuUsage)
			}
			if online == 0 {
				online = ncpu
			}
			if cpuD >= 0 && sysD > 0 {
				cs.cpu = cpuD / sysD * float64(online) * 100
				cs.hasCPU = true
			} else if cpuD >= 0 && cur.CPUStats.SystemUsage == 0 && dt > 0 {
				// some platforms don't report system usage; fall back to wall time
				cs.cpu = cpuD / (dt * 1e9) * 100
				cs.hasCPU = true
			}
			rx1, tx1 := cur.NetTotals()
			rx0, tx0 := prev.NetTotals()
			if rx1 >= rx0 && tx1 >= tx0 {
				cs.rx = float64(rx1-rx0) / dt
				cs.tx = float64(tx1-tx0) / dt
			}
		}
		next[id] = cs
		m.prev[id] = cur
	}
	for id := range m.prev {
		if _, ok := samples[id]; !ok {
			delete(m.prev, id)
		}
	}
	m.cstats = next
}

// sortVals are the sortable attributes of a container or compose group.
type sortVals struct {
	name    string
	state   int
	cpu     float64
	mem     float64
	net     float64
	pids    float64
	image   string
	created int64
}

func stateRank(s string) int {
	switch s {
	case "running":
		return 0
	case "restarting":
		return 1
	case "paused":
		return 2
	case "created":
		return 3
	case "exited":
		return 4
	default:
		return 5
	}
}

func (m *Model) containerVals(c *docker.Container) sortVals {
	v := sortVals{name: strings.ToLower(c.Name()), state: stateRank(c.State), image: c.Image, created: c.Created}
	if s := m.cstats[c.ID]; s != nil {
		v.cpu, v.mem, v.net, v.pids = s.cpu, float64(s.mem), s.rx+s.tx, float64(s.pids)
	}
	return v
}

func compareVals(a, b sortVals, key string) int {
	cmpF := func(x, y float64) int {
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	switch key {
	case "state":
		if a.state != b.state {
			return a.state - b.state
		}
	case "cpu":
		if c := cmpF(a.cpu, b.cpu); c != 0 {
			return c
		}
	case "mem":
		if c := cmpF(a.mem, b.mem); c != 0 {
			return c
		}
	case "net":
		if c := cmpF(a.net, b.net); c != 0 {
			return c
		}
	case "pids":
		if c := cmpF(a.pids, b.pids); c != 0 {
			return c
		}
	case "image":
		if c := strings.Compare(a.image, b.image); c != 0 {
			return c
		}
	case "created":
		if c := cmpF(float64(a.created), float64(b.created)); c != 0 {
			return c
		}
	}
	return strings.Compare(a.name, b.name)
}

type groupRef struct {
	project string
	all     []*docker.Container
	visible []*docker.Container
}

// item is a top-level entry: either a compose group or a standalone container.
type item struct {
	vals sortVals
	c    *docker.Container
	g    *groupRef
}

func (m *Model) containerRows() []row {
	st := &m.lists[tabContainers]
	terms := m.filterTerms()
	key, desc := st.sortBy, st.desc
	less := func(a, b sortVals) bool {
		c := compareVals(a, b, key)
		if desc {
			return c > 0
		}
		return c < 0
	}

	visible := func(c *docker.Container) bool {
		if !m.showAll && !c.IsUp() {
			return false
		}
		return matchTerms(terms, c.Name(), c.Image, c.Project(), c.Service(), c.State, c.HealthStatus(), docker.ShortID(c.ID))
	}

	var items []item
	groups := map[string]*groupRef{}
	for i := range m.containers {
		c := &m.containers[i]
		p := c.Project()
		if m.group && p != "" {
			g := groups[p]
			if g == nil {
				g = &groupRef{project: p}
				groups[p] = g
			}
			g.all = append(g.all, c)
			continue
		}
		if visible(c) {
			items = append(items, item{vals: m.containerVals(c), c: c})
		}
	}
	for _, g := range groups {
		projectMatch := len(terms) > 0 && matchTerms(terms, g.project)
		for _, c := range g.all {
			if visible(c) || (projectMatch && (m.showAll || c.IsUp())) {
				g.visible = append(g.visible, c)
			}
		}
		if len(g.visible) == 0 {
			continue
		}
		sort.SliceStable(g.visible, func(i, j int) bool {
			return less(m.containerVals(g.visible[i]), m.containerVals(g.visible[j]))
		})
		v := sortVals{name: strings.ToLower(g.project), state: 99}
		for _, c := range g.visible {
			cv := m.containerVals(c)
			v.cpu += cv.cpu
			v.mem += cv.mem
			v.net += cv.net
			v.pids += cv.pids
			if cv.state < v.state {
				v.state = cv.state
			}
			if cv.created > v.created {
				v.created = cv.created
			}
		}
		v.image = v.name
		items = append(items, item{vals: v, g: g})
	}
	sort.SliceStable(items, func(i, j int) bool { return less(items[i].vals, items[j].vals) })

	rows := make([]row, 0, len(m.containers)+len(groups))
	for _, it := range items {
		if it.c != nil {
			rows = append(rows, m.containerRow(it.c, ""))
			continue
		}
		g := it.g
		rows = append(rows, m.groupRow(g))
		if m.collapsed[g.project] && len(terms) == 0 {
			continue
		}
		for i, c := range g.visible {
			glyph := "├ "
			if i == len(g.visible)-1 {
				glyph = "└ "
			}
			rows = append(rows, m.containerRow(c, glyph))
		}
	}
	return rows
}

var reExit = regexp.MustCompile(`^Exited \((-?\d+)\)`)

func shortStatus(c *docker.Container) string {
	s := c.Status
	for _, h := range []string{" (healthy)", " (unhealthy)", " (health: starting)", " (Paused)"} {
		s = strings.Replace(s, h, "", 1)
	}
	if strings.HasPrefix(s, "Up ") {
		s = "up " + strings.TrimPrefix(s, "Up ")
	} else if mm := reExit.FindStringSubmatch(s); mm != nil {
		s = strings.TrimSpace(strings.TrimPrefix(s, mm[0]))
	}
	s = strings.NewReplacer("About a minute", "1m", "About an hour", "1h", "Less than a second", "<1s", "About a ", "1 ", "About an ", "1 ").Replace(s)
	s = strings.NewReplacer(" seconds", "s", " second", "s", " minutes", "m", " minute", "m", " hours", "h", " hour", "h",
		" days", "d", " day", "d", " weeks", "w", " week", "w", " months", "mo", " month", "mo", " years", "y", " year", "y").Replace(s)
	return s
}

func stateLabel(c *docker.Container) string {
	h := c.HealthStatus()
	switch {
	case c.State == "running" && h != "":
		return h
	case c.State == "exited":
		if mm := reExit.FindStringSubmatch(c.Status); mm != nil {
			return "exited(" + mm[1] + ")"
		}
	}
	return c.State
}

func (m *Model) containerRow(c *docker.Container, glyph string) row {
	name := c.Name()
	if glyph != "" {
		// inside a compose group: drop the redundant project prefix
		if p := c.Project(); p != "" && (strings.HasPrefix(name, p+"-") || strings.HasPrefix(name, p+"_")) {
			name = name[len(p)+1:]
		}
		name = sSubtle.Render(glyph) + name
	}
	health := c.HealthStatus()
	stStyle := stateStyle(c.State, health)
	label := stateLabel(c)
	if strings.HasPrefix(label, "exited(") && label != "exited(0)" {
		stStyle = sRed
	}
	cells := []string{
		name,
		stStyle.Render(stateDot(c.State) + " " + label),
		sSubtle.Render(fit("-", 12, true)),
		sSubtle.Render(fit("-", 12, true)),
		sSubtle.Render(fit("-", 13, true)),
		"",
		sSubtle.Render(c.Image),
		compactPorts(c.Ports),
		sSubtle.Render(shortStatus(c)),
	}
	if s := m.cstats[c.ID]; s != nil {
		if s.hasCPU {
			cells[2] = bar(s.cpu/100, 5) + " " + fit(fmt.Sprintf("%.1f%%", s.cpu), 6, true)
		} else {
			cells[2] = bar(0, 5) + " " + sSubtle.Render(fit("…", 6, true))
		}
		frac := 0.0
		if s.memLimit > 0 {
			frac = float64(s.mem) / float64(s.memLimit)
		}
		cells[3] = bar(frac, 5) + " " + fit(humanBytes(float64(s.mem)), 6, true)
		if s.hasCPU {
			cells[4] = fit(humanBytes(s.rx)+sSubtle.Render("/")+humanBytes(s.tx), 13, true)
		}
		cells[5] = fmt.Sprint(s.pids)
	}
	return row{key: "c:" + c.ID, cells: cells, ref: c}
}

func (m *Model) groupRow(g *groupRef) row {
	arrow := "▾ "
	if m.collapsed[g.project] {
		arrow = "▸ "
	}
	up := 0
	var cpu, mem, rx, tx float64
	var pids uint64
	hasStats := false
	for _, c := range g.all {
		if c.IsUp() {
			up++
		}
	}
	for _, c := range g.visible {
		if s := m.cstats[c.ID]; s != nil {
			hasStats = true
			cpu += s.cpu
			mem += float64(s.mem)
			rx += s.rx
			tx += s.tx
			pids += s.pids
		}
	}
	countStyle := sGreen
	switch {
	case up == 0:
		countStyle = sSubtle
	case up < len(g.all):
		countStyle = sYellow
	}
	cells := []string{
		sGroup.Render(arrow + g.project),
		countStyle.Render(fmt.Sprintf("%s %d/%d up", "■", up, len(g.all))),
		"", "", "", "", "", "", "",
	}
	if hasStats {
		cells[2] = sSubtle.Render(fit(fmt.Sprintf("Σ %.1f%%", cpu), 12, true))
		cells[3] = sSubtle.Render(fit("Σ "+humanBytes(mem), 12, true))
		cells[4] = sSubtle.Render(fit(humanBytes(rx)+"/"+humanBytes(tx), 13, true))
		cells[5] = sSubtle.Render(fmt.Sprint(pids))
	}
	if len(g.all) > 0 {
		if wd := g.all[0].Labels[docker.LabelWorkingDir]; wd != "" {
			cells[6] = sSubtle.Render(wd)
		}
	}
	return row{key: "g:" + g.project, cells: cells, group: true, ref: g}
}

// targets returns the containers the current selection refers to.
func (m *Model) targets() ([]*docker.Container, *groupRef) {
	r := m.selected()
	if r == nil {
		return nil, nil
	}
	switch v := r.ref.(type) {
	case *docker.Container:
		return []*docker.Container{v}, nil
	case *groupRef:
		return v.all, v
	}
	return nil, nil
}

func pick(cs []*docker.Container, pred func(*docker.Container) bool) (ids, names []string) {
	for _, c := range cs {
		if pred(c) {
			ids = append(ids, c.ID)
			names = append(names, c.Name())
		}
	}
	return
}

func (m *Model) containerKey(key string) tea.Cmd {
	cli := m.cli
	switch key {
	case ".", "A":
		m.showAll = !m.showAll
		m.rebuild()
		if m.showAll {
			m.setStatus(stInfo, "showing all containers")
		} else {
			m.setStatus(stInfo, "showing running containers only")
		}
		return nil
	case "g":
		m.group = !m.group
		m.rebuild()
		return nil
	case "P":
		m.setSort("cpu", true)
		return nil
	case "M":
		m.setSort("mem", true)
		return nil
	case "N":
		m.setSort("name", false)
		return nil
	case "T":
		m.setSort("created", true)
		return nil
	case "z", "Z":
		// collapse / expand all groups
		expand := false
		for _, r := range m.rows {
			if g, ok := r.ref.(*groupRef); ok && m.collapsed[g.project] {
				expand = true
			}
		}
		for _, r := range m.rows {
			if g, ok := r.ref.(*groupRef); ok {
				m.collapsed[g.project] = !expand
			}
		}
		m.rebuild()
		return nil
	}

	targets, grp := m.targets()
	if len(targets) == 0 {
		return nil
	}
	label := func() string {
		if grp != nil {
			return "project " + grp.project
		}
		return targets[0].Name()
	}

	switch key {
	case " ", "enter":
		if grp != nil {
			m.collapsed[grp.project] = !m.collapsed[grp.project]
			m.rebuild()
			return nil
		}
		if key == "enter" {
			return m.openInspectContainer(targets[0])
		}
		// space on a grouped container collapses its group
		if p := targets[0].Project(); m.group && p != "" {
			m.collapsed[p] = true
			m.lists[tabContainers].selKey = "g:" + p
			m.rebuild()
		}
	case "d":
		if grp != nil {
			return m.openInspectGroup(grp)
		}
		return m.openInspectContainer(targets[0])
	case "l":
		return m.openLogs(label(), targets)
	case "s", "e":
		c := targets[0]
		if grp != nil {
			m.setStatus(stInfo, "select a container to open a shell")
			return nil
		}
		if c.State != "running" {
			m.setStatus(stErr, "%s is not running", c.Name())
			return nil
		}
		return m.runSession(c, false)
	case "a":
		c := targets[0]
		if grp != nil {
			m.setStatus(stInfo, "select a container to attach")
			return nil
		}
		if c.State != "running" {
			m.setStatus(stErr, "%s is not running", c.Name())
			return nil
		}
		return m.runSession(c, true)
	case "u":
		ids, names := pick(targets, func(c *docker.Container) bool { return !c.IsUp() })
		if len(ids) == 0 {
			ids, names = pick(targets, func(c *docker.Container) bool { return c.State == "paused" })
			if len(ids) == 0 {
				m.setStatus(stInfo, "already running")
				return nil
			}
			return m.runAll("unpausing", "unpaused", ids, names, tabContainers, cli.ContainerUnpause)
		}
		return m.runAll("starting", "started", ids, names, tabContainers, cli.ContainerStart)
	case "x":
		ids, names := pick(targets, func(c *docker.Container) bool { return c.IsUp() })
		if len(ids) == 0 {
			m.setStatus(stInfo, "not running")
			return nil
		}
		return m.runAll("stopping", "stopped", ids, names, tabContainers, cli.ContainerStop)
	case "r":
		ids, names := pick(targets, func(c *docker.Container) bool { return true })
		return m.runAll("restarting", "restarted", ids, names, tabContainers, cli.ContainerRestart)
	case "p":
		ids, names := pick(targets, func(c *docker.Container) bool { return c.State == "running" })
		if len(ids) > 0 {
			return m.runAll("pausing", "paused", ids, names, tabContainers, cli.ContainerPause)
		}
		ids, names = pick(targets, func(c *docker.Container) bool { return c.State == "paused" })
		if len(ids) > 0 {
			return m.runAll("unpausing", "unpaused", ids, names, tabContainers, cli.ContainerUnpause)
		}
		m.setStatus(stInfo, "nothing to pause")
	case "ctrl+k", "K":
		ids, names := pick(targets, func(c *docker.Container) bool { return c.State == "running" || c.State == "restarting" })
		if len(ids) == 0 {
			m.setStatus(stInfo, "not running")
			return nil
		}
		m.ask(fmt.Sprintf("Kill %s?", describeOr(label(), names)), func() tea.Cmd {
			return m.runAll("killing", "killed", ids, names, tabContainers, cli.ContainerKill)
		})
	case "ctrl+d", "D", "delete":
		ids, names := pick(targets, func(c *docker.Container) bool { return true })
		prompt := fmt.Sprintf("Remove container %s?", names[0])
		if grp != nil {
			prompt = fmt.Sprintf("Remove all %d containers of project %s?", len(ids), grp.project)
		}
		if anyUp(targets) {
			prompt += " (running containers will be killed)"
		}
		m.ask(prompt, func() tea.Cmd {
			return m.runAll("removing", "removed", ids, names, tabContainers, func(ctx context.Context, id string) error {
				return cli.ContainerRemove(ctx, id, true, false)
			})
		})
	case "X":
		m.ask("Remove all stopped containers?", func() tea.Cmd {
			m.setStatus(stBusy, "pruning containers…")
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				n, space, err := cli.ContainerPrune(ctx)
				return actionMsg{text: fmt.Sprintf("pruned %s, reclaimed %s", plural(n, "container"), humanBytes(float64(space))), err: err, refresh: tabContainers}
			}
		})
	}
	return nil
}

func describeOr(label string, names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return fmt.Sprintf("%d containers of %s", len(names), label)
}

func anyUp(cs []*docker.Container) bool {
	for _, c := range cs {
		if c.IsUp() {
			return true
		}
	}
	return false
}

// ---- inspect

func (m *Model) openInspectContainer(c *docker.Container) tea.Cmd {
	m.mode = modeInspect
	m.inspect = newPager(c.Name(), []string{sSubtle.Render("  loading…")})
	cli := m.cli
	id := c.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cj, raw, err := cli.ContainerInspect(ctx, id)
		if err != nil {
			return inspectMsg{err: err}
		}
		lines := containerSummary(cj)
		lines = append(lines, "", sSubtle.Render("── docker inspect ──────────────────────────────"), "")
		lines = append(lines, jsonLines(raw)...)
		return inspectMsg{title: "container " + strings.TrimPrefix(cj.Name, "/"), lines: lines}
	}
}

func (m *Model) openInspectGroup(g *groupRef) tea.Cmd {
	m.mode = modeInspect
	var lines []string
	kv := func(k, v string) {
		if v != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", sJSONKey.Render(fit(k, 14, false)), v))
		}
	}
	lines = append(lines, "", "  "+sGroup.Render("compose project "+g.project), "")
	if len(g.all) > 0 {
		l := g.all[0].Labels
		kv("Working dir", l[docker.LabelWorkingDir])
		kv("Config files", l[docker.LabelConfig])
		kv("Compose", l["com.docker.compose.version"])
	}
	lines = append(lines, "", "  "+sBold.Render("Containers"))
	for _, c := range g.all {
		lines = append(lines, fmt.Sprintf("    %s %s %s %s",
			stateStyle(c.State, c.HealthStatus()).Render(stateDot(c.State)),
			fit(c.Name(), 32, false), fit(stateLabel(c), 12, false), sSubtle.Render(c.Image)))
	}
	nets := map[string]bool{}
	vols := map[string]bool{}
	for _, c := range g.all {
		for n := range c.NetworkSettings.Networks {
			nets[n] = true
		}
		for _, mt := range c.Mounts {
			if mt.Type == "volume" {
				vols[mt.Name] = true
			}
		}
	}
	if len(nets) > 0 {
		lines = append(lines, "", "  "+sBold.Render("Networks"))
		for _, n := range sortedKeys(nets) {
			lines = append(lines, "    "+n)
		}
	}
	if len(vols) > 0 {
		lines = append(lines, "", "  "+sBold.Render("Volumes"))
		for _, n := range sortedKeys(vols) {
			lines = append(lines, "    "+n)
		}
	}
	m.inspect = newPager("project "+g.project, lines)
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containerSummary(cj *docker.ContainerJSON) []string {
	var lines []string
	kv := func(k, v string) {
		if v != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", sJSONKey.Render(fit(k, 14, false)), v))
		}
	}
	section := func(t string) { lines = append(lines, "", "  "+sBold.Render(t)) }

	lines = append(lines, "")
	kv("Name", sBold.Render(strings.TrimPrefix(cj.Name, "/")))
	kv("ID", docker.ShortID(cj.ID))
	kv("Image", cj.Config.Image)
	state := cj.State.Status
	if cj.State.Health != nil {
		state += " · " + cj.State.Health.Status
	}
	if !cj.State.Running && cj.State.Status == "exited" {
		state += fmt.Sprintf(" · exit code %d", cj.State.ExitCode)
	}
	if cj.State.OOMKilled {
		state += " · OOM killed"
	}
	kv("State", stateStyle(cj.State.Status, "").Render(state))
	if cj.State.Error != "" {
		kv("Error", sRed.Render(cj.State.Error))
	}
	if t, err := time.Parse(time.RFC3339Nano, cj.Created); err == nil {
		kv("Created", t.Local().Format("2006-01-02 15:04:05")+sSubtle.Render(" ("+ago(t)+")"))
	}
	if t, err := time.Parse(time.RFC3339Nano, cj.State.StartedAt); err == nil && t.Year() > 1 {
		kv("Started", t.Local().Format("2006-01-02 15:04:05")+sSubtle.Render(" ("+ago(t)+")"))
	}
	if !cj.State.Running {
		if t, err := time.Parse(time.RFC3339Nano, cj.State.FinishedAt); err == nil && t.Year() > 1 {
			kv("Finished", t.Local().Format("2006-01-02 15:04:05")+sSubtle.Render(" ("+ago(t)+")"))
		}
	}
	cmd := append(append([]string{}, cj.Config.Entrypoint...), cj.Config.Cmd...)
	kv("Command", strings.Join(cmd, " "))
	kv("Working dir", cj.Config.WorkingDir)
	kv("User", cj.Config.User)
	rp := cj.HostConfig.RestartPolicy.Name
	if rp != "" && rp != "no" {
		rp += fmt.Sprintf(" (restarts: %d)", cj.RestartCount)
	}
	kv("Restart", rp)
	if cj.HostConfig.Memory > 0 {
		kv("Mem limit", humanBytes(float64(cj.HostConfig.Memory)))
	}
	if cj.HostConfig.NanoCpus > 0 {
		kv("CPU limit", fmt.Sprintf("%.2f", float64(cj.HostConfig.NanoCpus)/1e9))
	}
	if p := cj.Config.Labels[docker.LabelProject]; p != "" {
		kv("Compose", p+" / "+cj.Config.Labels[docker.LabelService])
	}

	if len(cj.NetworkSettings.Ports) > 0 {
		section("Ports")
		keys := make([]string, 0, len(cj.NetworkSettings.Ports))
		for k := range cj.NetworkSettings.Ports {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			binds := cj.NetworkSettings.Ports[k]
			if len(binds) == 0 {
				lines = append(lines, "    "+k+sSubtle.Render(" (not published)"))
				continue
			}
			var hs []string
			for _, b := range binds {
				hs = append(hs, b.HostIp+":"+b.HostPort)
			}
			lines = append(lines, "    "+k+" ← "+strings.Join(hs, ", "))
		}
	}
	if len(cj.NetworkSettings.Networks) > 0 {
		section("Networks")
		names := make([]string, 0)
		for n := range cj.NetworkSettings.Networks {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			nw := cj.NetworkSettings.Networks[n]
			lines = append(lines, fmt.Sprintf("    %s %s %s", fit(n, 24, false), fit(nw.IPAddress, 16, false), sSubtle.Render(nw.MacAddress)))
		}
	}
	if len(cj.Mounts) > 0 {
		section("Mounts")
		for _, mt := range cj.Mounts {
			src := mt.Source
			if mt.Type == "volume" && mt.Name != "" {
				src = mt.Name
			}
			ro := ""
			if !mt.RW {
				ro = sYellow.Render(" ro")
			}
			lines = append(lines, fmt.Sprintf("    %s %s → %s%s", sSubtle.Render(fit(mt.Type, 6, false)), src, mt.Destination, ro))
		}
	}
	if len(cj.Config.Env) > 0 {
		section("Environment")
		for _, e := range cj.Config.Env {
			k, v, _ := strings.Cut(e, "=")
			lines = append(lines, "    "+sJSONKey.Render(k)+"="+v)
		}
	}
	return lines
}
