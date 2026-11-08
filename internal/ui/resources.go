package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Arctize/ctop/internal/docker"
)

var imageColumns = []column{
	{title: "REPOSITORY:TAG", width: 30, flex: true, sort: "name"},
	{title: "ID", width: 12, prio: 3},
	{title: "SIZE", width: 8, right: true, sort: "size"},
	{title: "CREATED", width: 10, prio: 1, sort: "created"},
	{title: "USED BY", width: 24, prio: 2, sort: "used"},
}

var networkColumns = []column{
	{title: "NAME", width: 22, flex: true, sort: "name"},
	{title: "DRIVER", width: 8, sort: "driver"},
	{title: "SCOPE", width: 6, prio: 5},
	{title: "SUBNET", width: 18, prio: 2},
	{title: "PROJECT", width: 14, prio: 3, sort: "project"},
	{title: "CONTAINERS", width: 24, prio: 1, sort: "used"},
	{title: "CREATED", width: 10, prio: 4, sort: "created"},
}

var volumeColumns = []column{
	{title: "NAME", width: 26, flex: true, sort: "name"},
	{title: "DRIVER", width: 7, prio: 5, sort: "driver"},
	{title: "PROJECT", width: 14, prio: 3, sort: "project"},
	{title: "SIZE", width: 8, right: true, prio: 2, sort: "size"},
	{title: "USED BY", width: 24, prio: 1, sort: "used"},
	{title: "CREATED", width: 10, prio: 4, sort: "created"},
}

// resVals are the sortable attributes of images, networks and volumes.
type resVals struct {
	name, driver, project string
	size, used            float64
	created               int64
}

func sortRes(rows []row, vals []resVals, key string, desc bool) {
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := vals[idx[a]], vals[idx[b]]
		c := 0
		switch key {
		case "size":
			c = cmpF(x.size, y.size)
		case "used":
			c = cmpF(x.used, y.used)
		case "created":
			c = cmpF(float64(x.created), float64(y.created))
		case "driver":
			c = strings.Compare(x.driver, y.driver)
		case "project":
			c = strings.Compare(x.project, y.project)
		}
		if c == 0 {
			c = strings.Compare(x.name, y.name)
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	sorted := make([]row, len(rows))
	for i, j := range idx {
		sorted[i] = rows[j]
	}
	copy(rows, sorted)
}

func cmpF(x, y float64) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

func usedBy(names []string) string {
	switch len(names) {
	case 0:
		return sSubtle.Render("—")
	case 1:
		return names[0]
	}
	sort.Strings(names)
	return names[0] + sSubtle.Render(fmt.Sprintf(" +%d", len(names)-1))
}

// ---- images

func imageName(img *docker.Image) (string, bool) {
	for _, t := range img.RepoTags {
		if t != "<none>:<none>" {
			return t, true
		}
	}
	for _, d := range img.RepoDigests {
		repo, _, _ := strings.Cut(d, "@")
		return repo + ":<none>", false
	}
	return "<none>", false
}

func (m *Model) imageRows() []row {
	st := &m.lists[tabImages]
	terms := m.filterTerms()
	users := map[string][]string{}
	for i := range m.containers {
		c := &m.containers[i]
		users[c.ImageID] = append(users[c.ImageID], c.Name())
	}
	var rows []row
	var vals []resVals
	for i := range m.images {
		img := &m.images[i]
		name, tagged := imageName(img)
		if !matchTerms(terms, append([]string{name, docker.ShortID(img.ID)}, img.RepoTags...)...) {
			continue
		}
		u := users[img.ID]
		disp := name
		if !tagged {
			disp = sSubtle.Render(name)
		}
		if n := len(img.RepoTags); n > 1 {
			disp += sSubtle.Render(fmt.Sprintf(" +%d tags", n-1))
		}
		created := time.Unix(img.Created, 0)
		rows = append(rows, row{
			key: "i:" + img.ID,
			cells: []string{
				disp,
				sSubtle.Render(docker.ShortID(img.ID)),
				humanBytes(float64(img.Size)),
				sSubtle.Render(humanDuration(time.Since(created))),
				usedBy(u),
			},
			ref: img,
		})
		vals = append(vals, resVals{name: strings.ToLower(name), size: float64(img.Size), used: float64(len(u)), created: img.Created})
	}
	sortRes(rows, vals, st.sortBy, st.desc)
	return rows
}

// ---- networks

func (m *Model) networkUsers() map[string][]string {
	users := map[string][]string{}
	for i := range m.containers {
		c := &m.containers[i]
		for n, ep := range c.NetworkSettings.Networks {
			users[n] = append(users[n], c.Name())
			if ep.NetworkID != "" && ep.NetworkID != n {
				users[ep.NetworkID] = append(users[ep.NetworkID], c.Name())
			}
		}
	}
	return users
}

func (m *Model) networkRows() []row {
	st := &m.lists[tabNetworks]
	terms := m.filterTerms()
	users := m.networkUsers()
	var rows []row
	var vals []resVals
	for i := range m.networks {
		n := &m.networks[i]
		proj := n.Labels[docker.LabelProject]
		if !matchTerms(terms, n.Name, n.Driver, proj, docker.ShortID(n.ID)) {
			continue
		}
		var subnets []string
		for _, c := range n.IPAM.Config {
			if c.Subnet != "" {
				subnets = append(subnets, c.Subnet)
			}
		}
		u := users[n.ID]
		if len(u) == 0 {
			u = users[n.Name]
		}
		name := n.Name
		if n.Name == "bridge" || n.Name == "host" || n.Name == "none" {
			name = sSubtle.Render(n.Name)
		}
		if n.Internal {
			name += sSubtle.Render(" (internal)")
		}
		rows = append(rows, row{
			key: "n:" + n.ID,
			cells: []string{
				name, n.Driver, sSubtle.Render(n.Scope), strings.Join(subnets, ","),
				proj, usedBy(append([]string(nil), u...)), sSubtle.Render(humanDuration(time.Since(n.Created))),
			},
			ref: n,
		})
		vals = append(vals, resVals{name: strings.ToLower(n.Name), driver: n.Driver, project: proj, used: float64(len(u)), created: n.Created.Unix()})
	}
	sortRes(rows, vals, st.sortBy, st.desc)
	return rows
}

// ---- volumes

func (m *Model) volumeRows() []row {
	st := &m.lists[tabVolumes]
	terms := m.filterTerms()
	users := map[string][]string{}
	for i := range m.containers {
		c := &m.containers[i]
		for _, mt := range c.Mounts {
			if mt.Type == "volume" && mt.Name != "" {
				users[mt.Name] = append(users[mt.Name], c.Name())
			}
		}
	}
	var rows []row
	var vals []resVals
	for i := range m.volumes {
		v := &m.volumes[i]
		proj := v.Labels[docker.LabelProject]
		if !matchTerms(terms, v.Name, v.Driver, proj) {
			continue
		}
		name := v.Name
		if _, anon := v.Labels["com.docker.volume.anonymous"]; anon || (len(name) == 64 && isHex(name)) {
			name = sSubtle.Render(name)
		}
		size := sSubtle.Render("-")
		sz, ok := m.volSizes[v.Name]
		if ok && sz >= 0 {
			size = humanBytes(float64(sz))
		} else if m.dfLoading {
			size = sSubtle.Render("…")
		}
		created, _ := time.Parse(time.RFC3339, v.CreatedAt)
		age := "-"
		if !created.IsZero() {
			age = humanDuration(time.Since(created))
		}
		u := users[v.Name]
		rows = append(rows, row{
			key:   "v:" + v.Name,
			cells: []string{name, v.Driver, proj, size, usedBy(append([]string(nil), u...)), sSubtle.Render(age)},
			ref:   v,
		})
		vals = append(vals, resVals{name: strings.ToLower(v.Name), driver: v.Driver, project: proj, size: float64(sz), used: float64(len(u)), created: created.Unix()})
	}
	sortRes(rows, vals, st.sortBy, st.desc)
	return rows
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// ---- actions

func (m *Model) resourceKey(key string) tea.Cmd {
	cli := m.cli
	if key == "X" {
		return m.prune()
	}
	r := m.selected()
	if r == nil {
		return nil
	}
	switch ref := r.ref.(type) {
	case *docker.Image:
		name, _ := imageName(ref)
		id := ref.ID
		switch key {
		case "enter", "d":
			return m.openInspectRaw("image "+name, func(ctx context.Context) ([]byte, error) { return cli.ImageInspectRaw(ctx, id) })
		case "ctrl+d", "D", "delete":
			target := id
			if len(ref.RepoTags) == 1 && ref.RepoTags[0] != "<none>:<none>" {
				target = ref.RepoTags[0]
			}
			m.ask(fmt.Sprintf("Remove image %s?", name), func() tea.Cmd {
				return m.runAll("removing", "removed", []string{target}, []string{name}, tabImages, func(ctx context.Context, id string) error {
					return cli.ImageRemove(ctx, id, false)
				})
			})
		case "F":
			m.ask(fmt.Sprintf("Force remove image %s (all tags)?", name), func() tea.Cmd {
				return m.runAll("removing", "removed", []string{id}, []string{name}, tabImages, func(ctx context.Context, id string) error {
					return cli.ImageRemove(ctx, id, true)
				})
			})
		}
	case *docker.Network:
		id, name := ref.ID, ref.Name
		switch key {
		case "enter", "d":
			return m.openInspectRaw("network "+name, func(ctx context.Context) ([]byte, error) { return cli.NetworkInspectRaw(ctx, id) })
		case "ctrl+d", "D", "delete":
			m.ask(fmt.Sprintf("Remove network %s?", name), func() tea.Cmd {
				return m.runAll("removing", "removed", []string{id}, []string{name}, tabNetworks, cli.NetworkRemove)
			})
		}
	case *docker.Volume:
		name := ref.Name
		switch key {
		case "enter", "d":
			return m.openInspectRaw("volume "+name, func(ctx context.Context) ([]byte, error) { return cli.VolumeInspectRaw(ctx, name) })
		case "ctrl+d", "D", "delete":
			m.ask(fmt.Sprintf("Remove volume %s? Its data will be lost.", name), func() tea.Cmd {
				return m.runAll("removing", "removed", []string{name}, []string{name}, tabVolumes, func(ctx context.Context, id string) error {
					return cli.VolumeRemove(ctx, id, false)
				})
			})
		}
	}
	return nil
}

func (m *Model) prune() tea.Cmd {
	cli := m.cli
	t := m.tab
	var prompt string
	var fn func(ctx context.Context) (string, error)
	switch t {
	case tabImages:
		prompt = "Remove all dangling images?"
		fn = func(ctx context.Context) (string, error) {
			n, space, err := cli.ImagePrune(ctx, false)
			return fmt.Sprintf("pruned %s, reclaimed %s", plural(n, "image"), humanBytes(float64(space))), err
		}
	case tabNetworks:
		prompt = "Remove all unused networks?"
		fn = func(ctx context.Context) (string, error) {
			n, err := cli.NetworkPrune(ctx)
			return fmt.Sprintf("pruned %s", plural(n, "network")), err
		}
	case tabVolumes:
		prompt = "Remove all unused anonymous volumes?"
		fn = func(ctx context.Context) (string, error) {
			n, space, err := cli.VolumePrune(ctx, false)
			return fmt.Sprintf("pruned %s, reclaimed %s", plural(n, "volume"), humanBytes(float64(space))), err
		}
	default:
		return nil
	}
	m.ask(prompt, func() tea.Cmd {
		m.setStatus(stBusy, "pruning…")
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			text, err := fn(ctx)
			return actionMsg{text: text, err: err, refresh: t}
		}
	})
	return nil
}

func (m *Model) openInspectRaw(title string, get func(ctx context.Context) ([]byte, error)) tea.Cmd {
	m.mode = modeInspect
	m.inspect = newPager(title, []string{sSubtle.Render("  loading…")})
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		raw, err := get(ctx)
		if err != nil {
			return inspectMsg{err: err}
		}
		return inspectMsg{title: title, lines: jsonLines(raw)}
	}
}
