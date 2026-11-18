package ui

import (
	"fmt"
	"strings"
)

func (m *Model) viewSystem(h int) []string {
	var lines []string
	kv := func(k, v string) {
		if v != "" {
			lines = append(lines, fmt.Sprintf("   %s %s", sJSONKey.Render(fit(k, 16, false)), v))
		}
	}
	section := func(t string) { lines = append(lines, "", " "+sGroup.Render(t)) }

	if m.info == nil {
		return []string{"", sSubtle.Render("  loading…")}
	}
	i := m.info
	section("Engine")
	kv("Endpoint", m.cli.Host)
	if v := m.version; v != nil {
		kv("Version", fmt.Sprintf("%s %s", v.Version, sSubtle.Render(fmt.Sprintf("(API %s, min %s, %s)", v.APIVersion, v.MinAPIVersion, v.GoVersion))))
		for _, c := range v.Components {
			if c.Name != "Engine" {
				kv(c.Name, componentVersion(c.Version))
			}
		}
	}
	kv("Storage driver", i.Driver)
	kv("Logging driver", i.LoggingDriver)
	kv("Cgroup", strings.Trim(i.CgroupDriver+" v"+i.CgroupVersion, " v"))
	kv("Runtime", i.DefaultRuntime)
	kv("Root dir", i.DockerRootDir)
	if i.Swarm.LocalNodeState != "" && i.Swarm.LocalNodeState != "inactive" {
		kv("Swarm", i.Swarm.LocalNodeState)
	}
	var sec []string
	for _, s := range i.SecurityOptions {
		if name, _, ok := strings.Cut(strings.TrimPrefix(s, "name="), ","); ok || name != "" {
			sec = append(sec, name)
		}
	}
	kv("Security", strings.Join(sec, ", "))

	section("Host")
	kv("Name", i.Name)
	kv("OS", i.OperatingSystem)
	kv("Kernel", i.KernelVersion)
	kv("Arch", i.Architecture)
	kv("CPUs", fmt.Sprint(i.NCPU))
	kv("Memory", humanBytes(float64(i.MemTotal)))

	section("Objects")
	kv("Containers", fmt.Sprintf("%d %s", i.Containers,
		sSubtle.Render(fmt.Sprintf("(%d running, %d paused, %d stopped)", i.ContainersRunning, i.ContainersPaused, i.ContainersStopped))))
	kv("Images", fmt.Sprint(i.Images))

	section("Disk usage")
	if m.df == nil {
		if m.dfLoading {
			lines = append(lines, sSubtle.Render("   calculating…"))
		} else {
			lines = append(lines, sSubtle.Render("   press ctrl+r to calculate"))
		}
	} else {
		d := m.df
		row := func(a, b, c, e, f string) string {
			return "   " + fit(a, 14, false) + fit(b, 8, true) + fit(c, 9, true) + fit(e, 11, true) + fit(f, 14, true)
		}
		lines = append(lines, sSubtle.Render(row("TYPE", "TOTAL", "ACTIVE", "SIZE", "RECLAIMABLE")))
		var imgActive int
		var imgReclaim int64
		for _, im := range d.Images {
			if im.Containers > 0 {
				imgActive++
			} else {
				imgReclaim += im.Size
			}
		}
		lines = append(lines, row("Images", fmt.Sprint(len(d.Images)), fmt.Sprint(imgActive), humanBytes(float64(d.LayersSize)), humanBytes(float64(imgReclaim))))
		var cActive int
		var cSize, cReclaim int64
		for _, c := range d.Containers {
			cSize += c.SizeRw
			if c.State == "running" || c.State == "paused" || c.State == "restarting" {
				cActive++
			} else {
				cReclaim += c.SizeRw
			}
		}
		lines = append(lines, row("Containers", fmt.Sprint(len(d.Containers)), fmt.Sprint(cActive), humanBytes(float64(cSize)), humanBytes(float64(cReclaim))))
		var vActive int
		var vSize, vReclaim int64
		for _, v := range d.Volumes {
			if v.UsageData == nil {
				continue
			}
			if v.UsageData.Size > 0 {
				vSize += v.UsageData.Size
			}
			if v.UsageData.RefCount > 0 {
				vActive++
			} else if v.UsageData.Size > 0 {
				vReclaim += v.UsageData.Size
			}
		}
		lines = append(lines, row("Volumes", fmt.Sprint(len(d.Volumes)), fmt.Sprint(vActive), humanBytes(float64(vSize)), humanBytes(float64(vReclaim))))
		var bActive int
		var bSize, bReclaim int64
		for _, b := range d.BuildCache {
			bSize += b.Size
			if b.InUse {
				bActive++
			} else if !b.Shared {
				bReclaim += b.Size
			}
		}
		lines = append(lines, row("Build cache", fmt.Sprint(len(d.BuildCache)), fmt.Sprint(bActive), humanBytes(float64(bSize)), humanBytes(float64(bReclaim))))
	}

	if len(i.Warnings) > 0 {
		section("Warnings")
		for _, w := range i.Warnings {
			lines = append(lines, "   "+sYellow.Render(w))
		}
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lines
}

// componentVersion tidies component version strings: podman reports whole
// "--version" outputs, e.g. "crun version 1.2\ncommit: ...\n+SYSTEMD ...".
func componentVersion(v string) string {
	v, _, _ = strings.Cut(strings.TrimSpace(v), "\n")
	if _, after, ok := strings.Cut(v, " version "); ok {
		v = after
	}
	return strings.TrimSpace(v)
}

var helpSections = []struct {
	title string
	keys  [][2]string
}{
	{"Global", [][2]string{
		{"1-5 / tab / ←→", "switch view"},
		{"↑↓ jk  pgup/pgdn  home/end", "move"},
		{"/", "filter (space separated terms, all must match)"},
		{"esc", "clear filter / go back"},
		{"< >  F6", "change sort column"},
		{"i", "invert sort order"},
		{"ctrl+r  F5", "refresh now"},
		{"?", "this help"},
		{"q  ctrl+c", "quit"},
	}},
	{"Containers", [][2]string{
		{"enter", "inspect container / fold compose project"},
		{"d", "inspect (on a project: project overview)"},
		{"l", "logs (on a project: merged logs of all services)"},
		{"s", "open a shell (bash → ash → sh)"},
		{"a", "attach (detach with ctrl-p ctrl-q)"},
		{"u / x / r", "start / stop / restart (project-wide on a header)"},
		{"p", "pause / unpause"},
		{"ctrl+k", "kill"},
		{"ctrl+d  D", "remove (force)"},
		{"X", "prune stopped containers"},
		{". ", "toggle stopped containers"},
		{"g", "toggle compose grouping"},
		{"space / z", "fold project / fold all"},
		{"P M N T", "sort by cpu / mem / name / age"},
	}},
	{"Images · Networks · Volumes", [][2]string{
		{"enter  d", "inspect"},
		{"ctrl+d  D", "remove (F: force remove image)"},
		{"X", "prune unused"},
	}},
	{"Logs", [][2]string{
		{"f", "toggle follow"},
		{"w / t", "toggle wrap / timestamps"},
		{"/", "filter lines"},
		{"←→  g/G", "pan  /  top, bottom"},
		{"c", "clear buffer"},
	}},
}

func (m *Model) viewHelp(h int) []string {
	var left, right []string
	for i, s := range helpSections {
		var out []string
		out = append(out, "", " "+sGroup.Render(s.title))
		for _, k := range s.keys {
			out = append(out, "   "+sKey.Render(fit(k[0], 28, false))+" "+k[1])
		}
		if i == 0 || i >= 2 {
			left = append(left, out...)
		} else {
			right = append(right, out...)
		}
	}
	if m.w < 150 {
		all := append(left, right...)
		if len(all) > h {
			all = all[:h]
		}
		return all
	}
	var lines []string
	for i := 0; i < len(left) || i < len(right); i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines = append(lines, fit(l, m.w/2, false)+r)
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lines
}
