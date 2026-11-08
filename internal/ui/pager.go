package ui

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// pager is a simple scrollable text view used for inspect output.
type pager struct {
	title     string
	lines     []string
	top, hoff int
	input     textinput.Model
	searching bool
	query     string
	lastH     int
}

func newPager(title string, lines []string) *pager {
	in := textinput.New()
	in.Prompt = "search: "
	return &pager{title: title, lines: lines, input: in}
}

func (p *pager) maxTop(h int) int {
	n := len(p.lines) - (h - 1)
	if n < 0 {
		return 0
	}
	return n
}

func (p *pager) scroll(d, h int) {
	p.top += d
	if p.top > p.maxTop(h) {
		p.top = p.maxTop(h)
	}
	if p.top < 0 {
		p.top = 0
	}
}

func (p *pager) find(from, dir int) {
	if p.query == "" || len(p.lines) == 0 {
		return
	}
	q := strings.ToLower(p.query)
	n := len(p.lines)
	for i := 1; i <= n; i++ {
		idx := ((from+dir*i)%n + n) % n
		if strings.Contains(strings.ToLower(ansi.Strip(p.lines[idx])), q) {
			p.top = idx
			p.scroll(0, p.lastH)
			return
		}
	}
}

func (p *pager) view(w, h int) []string {
	p.lastH = h
	title := " " + sBold.Render(p.title)
	if len(p.lines) > h-1 {
		pct := 100
		if mt := p.maxTop(h); mt > 0 {
			pct = p.top * 100 / mt
		}
		title = joinLR(title, sSubtle.Render(strconv.Itoa(pct)+"% "), w)
	}
	out := []string{sColHeader.Render(fit(title, w, false))}
	q := strings.ToLower(p.query)
	for i := p.top; i < len(p.lines) && len(out) < h; i++ {
		l := p.lines[i]
		if p.hoff > 0 {
			l = ansi.Cut(l, p.hoff, p.hoff+w)
		}
		if q != "" && strings.Contains(strings.ToLower(ansi.Strip(l)), q) {
			l = sSelected.Render(fit(ansi.Strip(l), w, false))
		}
		out = append(out, l)
	}
	return out
}

func (m *Model) inspectKey(msg tea.KeyMsg) tea.Cmd {
	p := m.inspect
	h := m.bodyHeight()
	if p.searching {
		switch msg.String() {
		case "enter":
			p.searching = false
			p.query = p.input.Value()
			p.input.Blur()
			p.find(p.top-1, 1)
			return nil
		case "esc":
			p.searching = false
			p.input.Blur()
			return nil
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return cmd
	}
	switch msg.String() {
	case "esc", "q", "backspace":
		if p.query != "" && msg.String() == "esc" {
			p.query = ""
			return nil
		}
		m.mode = modeList
		m.inspect = nil
	case "up", "k":
		p.scroll(-1, h)
	case "down", "j":
		p.scroll(1, h)
	case "pgup", "ctrl+b", "ctrl+u":
		p.scroll(-(h - 2), h)
	case "pgdown", "ctrl+f", " ":
		p.scroll(h-2, h)
	case "home", "g":
		p.top = 0
	case "end", "G":
		p.top = p.maxTop(h)
	case "left", "h":
		p.hoff -= 8
		if p.hoff < 0 {
			p.hoff = 0
		}
	case "right", "l":
		p.hoff += 8
	case "/":
		p.searching = true
		p.input.SetValue("")
		return p.input.Focus()
	case "n":
		p.find(p.top, 1)
	case "N":
		p.find(p.top, -1)
	}
	return nil
}

var reJSONLine = regexp.MustCompile(`^(\s*)(?:("(?:[^"\\]|\\.)*")(\s*:\s*))?(.*?)(,?)$`)

// jsonLines pretty prints and highlights raw JSON.
func jsonLines(raw []byte) []string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return strings.Split(string(raw), "\n")
	}
	src := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i := range src {
		src[i] = "  " + src[i]
	}
	out := make([]string, 0, len(src))
	for _, l := range src {
		mm := reJSONLine.FindStringSubmatch(l)
		if mm == nil {
			out = append(out, l)
			continue
		}
		indent, key, colon, val, comma := mm[1], mm[2], mm[3], mm[4], mm[5]
		var b strings.Builder
		b.WriteString(indent)
		if key != "" {
			b.WriteString(sJSONKey.Render(key))
			b.WriteString(colon)
		}
		switch {
		case strings.HasPrefix(val, `"`):
			b.WriteString(sJSONStr.Render(sanitize(val)))
		case val == "{" || val == "}" || val == "[" || val == "]" || val == "{}" || val == "[]":
			b.WriteString(sSubtle.Render(val))
		case val != "":
			b.WriteString(sJSONLit.Render(val))
		}
		b.WriteString(sSubtle.Render(comma))
		out = append(out, b.String())
	}
	return out
}
