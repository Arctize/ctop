package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Arctize/ctop/internal/docker"
)

func humanBytes(b float64) string {
	const unit = 1024.0
	if b < unit {
		return fmt.Sprintf("%.0fB", b)
	}
	units := "KMGTPE"
	i := -1
	for b >= unit && i < len(units)-1 {
		b /= unit
		i++
	}
	if b >= 100 {
		return fmt.Sprintf("%.0f%c", b, units[i])
	}
	return fmt.Sprintf("%.1f%c", b, units[i])
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 2*365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return humanDuration(time.Since(t)) + " ago"
}

// bar renders a horizontal meter of the given width using eighth blocks.
func bar(frac float64, width int) string {
	if width <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	eighths := int(frac*float64(width*8) + 0.5)
	full, rem := eighths/8, eighths%8
	var b strings.Builder
	b.WriteString(strings.Repeat("█", full))
	n := full
	if rem > 0 && n < width {
		b.WriteRune([]rune(" ▏▎▍▌▋▊▉")[rem])
		n++
	}
	return levelStyle(frac).Render(b.String()) + sSubtle.Render(strings.Repeat("·", width-n))
}

func levelStyle(frac float64) lipgloss.Style {
	switch {
	case frac >= 0.85:
		return sRed
	case frac >= 0.6:
		return sYellow
	default:
		return sGreen
	}
}

// compactPorts renders published ports as "8080→80,443→443/udp", deduping
// the IPv4/IPv6 pairs docker reports.
func compactPorts(ports []docker.Port) string {
	seen := map[string]bool{}
	var out []string
	sorted := append([]docker.Port(nil), ports...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].PublicPort != sorted[j].PublicPort {
			return sorted[i].PublicPort < sorted[j].PublicPort
		}
		return sorted[i].PrivatePort < sorted[j].PrivatePort
	})
	for _, p := range sorted {
		if p.PublicPort == 0 {
			continue
		}
		s := fmt.Sprintf("%d→%d", p.PublicPort, p.PrivatePort)
		if p.PublicPort == p.PrivatePort {
			s = fmt.Sprintf("%d", p.PublicPort)
		}
		if p.Type != "" && p.Type != "tcp" {
			s += "/" + p.Type
		}
		if p.IP != "" && p.IP != "0.0.0.0" && p.IP != "::" {
			s = p.IP + ":" + s
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}

var (
	reCSI     = regexp.MustCompile(`\x1b\[[0-9;:?<=>]*[ -/]*[@-~]`)
	reOSC     = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	reOtherEs = regexp.MustCompile(`\x1b[@-Z\\-_]`)
)

// sanitize makes arbitrary program output safe to embed in the TUI: it keeps
// SGR color sequences, drops all other escapes and control characters and
// expands tabs.
func sanitize(s string) string {
	if s == "" {
		return s
	}
	s = strings.TrimRight(s, "\r")
	if i := strings.LastIndexByte(s, '\r'); i >= 0 { // progress-bar style overwrites
		s = s[i+1:]
	}
	hasEsc := strings.IndexByte(s, 0x1b) >= 0
	if hasEsc {
		s = reOSC.ReplaceAllString(s, "")
		s = reCSI.ReplaceAllStringFunc(s, func(m string) string {
			if strings.HasSuffix(m, "m") {
				return m
			}
			return ""
		})
		s = reOtherEs.ReplaceAllString(s, "")
	}
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != 0x1b) || c == 0x7f {
			clean = false
			break
		}
	}
	if !clean {
		var b strings.Builder
		for _, r := range s {
			switch {
			case r == '\t':
				b.WriteString("    ")
			case r == 0x1b:
				b.WriteRune(r)
			case r < 0x20 || r == 0x7f:
			default:
				b.WriteRune(r)
			}
		}
		s = b.String()
	}
	if hasEsc {
		s += "\x1b[0m"
	}
	return s
}

// fit pads or truncates s (which may contain ANSI) to exactly w cells.
func fit(s string, w int, right bool) string {
	if w <= 0 {
		return ""
	}
	sw := ansi.StringWidth(s)
	if sw > w {
		s = ansi.Truncate(s, w, "…")
		sw = ansi.StringWidth(s)
	}
	pad := strings.Repeat(" ", w-sw)
	if right {
		return pad + s
	}
	return s + pad
}

// joinLR places left and right on one line of width w.
func joinLR(left, right string, w int) string {
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if lw+rw+1 > w {
		return fit(left, w, false)
	}
	return left + strings.Repeat(" ", w-lw-rw) + right
}

func plural(n int, s string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, s)
	}
	return fmt.Sprintf("%d %ss", n, s)
}
