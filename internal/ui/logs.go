package ui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Arctize/ctop/internal/docker"
)

const maxLogLines = 20000

const streamInfo byte = 255 // synthetic lines from ctop itself

type logLine struct {
	ts     time.Time
	src    int
	stream byte
	text   string
}

type logBatchMsg struct {
	gen   int
	lines []logLine
}

type logEndMsg struct{ gen int }

type logView struct {
	title   string
	sources []string
	srcW    int
	lines   []logLine
	gen     int
	ch      chan logLine
	cancel  context.CancelFunc

	follow, wrap, showTS bool
	top, hoff            int // top is an index into the filtered lines

	filter    string
	filtering bool
	input     textinput.Model
	ended     bool
}

func waitLog(gen int, ch chan logLine) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-ch
		if !ok {
			return logEndMsg{gen: gen}
		}
		batch := []logLine{l}
		for len(batch) < 2000 {
			select {
			case l, ok := <-ch:
				if !ok {
					return logBatchMsg{gen: gen, lines: batch}
				}
				batch = append(batch, l)
			default:
				return logBatchMsg{gen: gen, lines: batch}
			}
		}
		return logBatchMsg{gen: gen, lines: batch}
	}
}

func (m *Model) closeLogs() {
	if m.logs != nil && m.logs.cancel != nil {
		m.logs.cancel()
	}
	m.logs = nil
	if m.mode == modeLogs {
		m.mode = modeList
	}
}

func (m *Model) openLogs(title string, targets []*docker.Container) tea.Cmd {
	m.closeLogs()
	m.logGen++
	ctx, cancel := context.WithCancel(context.Background())
	in := textinput.New()
	in.Prompt = "filter logs: "
	lv := &logView{
		title:  title,
		gen:    m.logGen,
		ch:     make(chan logLine, 4096),
		cancel: cancel,
		follow: true,
		input:  in,
	}
	for _, c := range targets {
		name := c.Name()
		if p := c.Project(); p != "" && len(targets) > 1 {
			name = strings.TrimPrefix(strings.TrimPrefix(name, p+"-"), p+"_")
		}
		lv.sources = append(lv.sources, name)
		if w := ansi.StringWidth(name); w > lv.srcW {
			lv.srcW = w
		}
	}
	if lv.srcW > 24 {
		lv.srcW = 24
	}
	m.logs = lv
	m.mode = modeLogs

	tail := m.cfg.Tail
	if len(targets) > 1 {
		tail = tail / len(targets)
		if tail < 100 {
			tail = 100
		}
	}
	cli := m.cli
	ch := lv.ch
	var wg sync.WaitGroup
	for i, c := range targets {
		wg.Add(1)
		go func(src int, id, name string) {
			defer wg.Done()
			send := func(l logLine) bool {
				select {
				case ch <- l:
					return true
				case <-ctx.Done():
					return false
				}
			}
			info := func(s string) { send(logLine{ts: time.Now(), src: src, stream: streamInfo, text: s}) }
			cj, _, err := cli.ContainerInspect(ctx, id)
			if err != nil {
				if ctx.Err() == nil {
					info("error: " + err.Error())
				}
				return
			}
			rc, err := cli.ContainerLogs(ctx, id, docker.LogOptions{Tail: strconv.Itoa(tail), Follow: true, Timestamps: true})
			if err != nil {
				if ctx.Err() == nil {
					info("error: " + err.Error())
				}
				return
			}
			defer rc.Close()
			err = docker.ReadLines(rc, cj.Config.Tty, func(stream byte, line string) {
				l := logLine{src: src, stream: stream}
				if i := strings.IndexByte(line, ' '); i > 0 && i < 40 {
					if ts, err := time.Parse(time.RFC3339Nano, line[:i]); err == nil {
						l.ts = ts
						line = line[i+1:]
					}
				}
				l.text = sanitize(line)
				send(l)
			})
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				info("log stream error: " + err.Error())
			} else {
				info("log stream ended (container stopped)")
			}
		}(i, c.ID, c.Name())
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	return waitLog(lv.gen, ch)
}

func (lv *logView) add(batch []logLine) {
	if !lv.follow && lv.filter != "" {
	}
	for _, l := range batch {
		n := len(lv.lines)
		if n == 0 || l.ts.IsZero() || !l.ts.Before(lv.lines[n-1].ts) {
			lv.lines = append(lv.lines, l)
			continue
		}
		// keep merged logs ordered by timestamp
		i := sort.Search(n, func(i int) bool { return lv.lines[i].ts.After(l.ts) })
		lv.lines = append(lv.lines, logLine{})
		copy(lv.lines[i+1:], lv.lines[i:])
		lv.lines[i] = l
		if !lv.follow && lv.filter == "" && i < lv.top {
			lv.top++
		}
	}
	if over := len(lv.lines) - maxLogLines; over > 0 {
		lv.lines = append([]logLine(nil), lv.lines[over:]...)
		if !lv.follow {
			lv.top -= over
			if lv.top < 0 {
				lv.top = 0
			}
		}
	}
}

func (lv *logView) visible() []int {
	idx := make([]int, 0, len(lv.lines))
	f := strings.ToLower(lv.filter)
	for i := range lv.lines {
		if f == "" || strings.Contains(strings.ToLower(ansi.Strip(lv.lines[i].text)), f) ||
			(len(lv.sources) > 1 && strings.Contains(strings.ToLower(lv.sources[lv.lines[i].src]), f)) {
			idx = append(idx, i)
		}
	}
	return idx
}

func (lv *logView) flags() string {
	var f []string
	if lv.follow {
		f = append(f, sGreen.Render("following"))
	} else {
		f = append(f, sYellow.Render("paused"))
	}
	if lv.wrap {
		f = append(f, "wrap")
	}
	if lv.showTS {
		f = append(f, "timestamps")
	}
	if lv.filter != "" {
		f = append(f, sYellow.Render("filter: "+lv.filter))
	}
	f = append(f, fmt.Sprintf("%d lines", len(lv.lines)))
	return strings.Join(f, " · ")
}

func (lv *logView) prefix(l *logLine) string {
	var b strings.Builder
	if lv.showTS && !l.ts.IsZero() {
		b.WriteString(sSubtle.Render(l.ts.Local().Format("01-02 15:04:05.000")) + " ")
	}
	if len(lv.sources) > 1 {
		st := logPalette[l.src%len(logPalette)]
		b.WriteString(st.Render(fit(lv.sources[l.src], lv.srcW, false)) + sSubtle.Render(" │ "))
	}
	return b.String()
}

func (lv *logView) render(l *logLine) string {
	text := l.text
	switch l.stream {
	case docker.Stderr:
		if !strings.Contains(text, "\x1b[") {
			text = sRed.Faint(true).Render(text)
		}
	case streamInfo:
		text = sSubtle.Italic(true).Render("── " + text + " ──")
	}
	return lv.prefix(l) + text
}

// displayLines turns a log line into one or more screen lines.
func (lv *logView) displayLines(l *logLine, w int) []string {
	s := lv.render(l)
	if lv.wrap {
		return strings.Split(ansi.Hardwrap(s, w, true), "\n")
	}
	if lv.hoff > 0 {
		s = ansi.Cut(s, lv.hoff, lv.hoff+w)
	}
	return []string{s}
}

func (lv *logView) view(w, h int) []string {
	title := " " + sBold.Render("logs ") + sAccent.Render(lv.title)
	if lv.ended {
		title += sSubtle.Render("  (stream closed)")
	}
	out := []string{sColHeader.Render(fit(title, w, false))}
	body := h - 1
	vis := lv.visible()
	if len(vis) == 0 {
		msg := "waiting for logs…"
		if lv.filter != "" {
			msg = "no lines match the filter"
		} else if lv.ended {
			msg = "no logs"
		}
		return append(out, "", sSubtle.Render("  "+msg))
	}
	if lv.follow {
		var rev []string
		for i := len(vis) - 1; i >= 0 && len(rev) < body; i-- {
			dl := lv.displayLines(&lv.lines[vis[i]], w)
			for j := len(dl) - 1; j >= 0 && len(rev) < body; j-- {
				rev = append(rev, dl[j])
			}
		}
		for i := len(rev) - 1; i >= 0; i-- {
			out = append(out, rev[i])
		}
		return out
	}
	if lv.top > len(vis)-body {
		lv.top = len(vis) - body
	}
	if lv.top < 0 {
		lv.top = 0
	}
	for i := lv.top; i < len(vis) && len(out) < h; i++ {
		for _, d := range lv.displayLines(&lv.lines[vis[i]], w) {
			if len(out) >= h {
				break
			}
			out = append(out, d)
		}
	}
	return out
}

func (lv *logView) scroll(d, h int) {
	n := len(lv.visible())
	body := h - 1
	if lv.follow {
		if d >= 0 {
			return
		}
		lv.follow = false
		lv.top = n - body
	}
	lv.top += d
	if lv.top < 0 {
		lv.top = 0
	}
	if lv.top >= n-body {
		lv.top = n - body
		if lv.top < 0 {
			lv.top = 0
		}
		if d > 0 {
			lv.follow = true
		}
	}
}

func (m *Model) logsKey(msg tea.KeyMsg) tea.Cmd {
	lv := m.logs
	h := m.bodyHeight()
	if lv.filtering {
		switch msg.String() {
		case "enter":
			lv.filtering = false
			lv.input.Blur()
		case "esc":
			lv.filtering = false
			lv.input.Blur()
			lv.input.SetValue("")
			lv.filter = ""
		default:
			var cmd tea.Cmd
			lv.input, cmd = lv.input.Update(msg)
			lv.filter = lv.input.Value()
			return cmd
		}
		return nil
	}
	switch msg.String() {
	case "esc", "q", "backspace":
		if msg.String() == "esc" && lv.filter != "" {
			lv.filter = ""
			lv.input.SetValue("")
			return nil
		}
		m.closeLogs()
		m.mode = modeList
	case "up", "k":
		lv.scroll(-1, h)
	case "down", "j":
		lv.scroll(1, h)
	case "pgup", "ctrl+b", "ctrl+u":
		lv.scroll(-(h - 2), h)
	case "pgdown", "ctrl+f", " ":
		lv.scroll(h-2, h)
	case "home", "g":
		lv.follow = false
		lv.top = 0
	case "end", "G":
		lv.follow = true
	case "f":
		lv.follow = !lv.follow
		if !lv.follow {
			lv.top = len(lv.visible()) - (h - 1)
			if lv.top < 0 {
				lv.top = 0
			}
		}
	case "w":
		lv.wrap = !lv.wrap
		lv.hoff = 0
	case "t":
		lv.showTS = !lv.showTS
	case "left", "h":
		lv.hoff -= 10
		if lv.hoff < 0 {
			lv.hoff = 0
		}
	case "right", "l":
		if !lv.wrap {
			lv.hoff += 10
		}
	case "c":
		lv.lines = nil
		lv.top = 0
	case "/":
		lv.filtering = true
		lv.input.SetValue(lv.filter)
		return lv.input.Focus()
	}
	return nil
}
