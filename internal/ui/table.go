package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// column describes a table column. Columns with a higher prio are dropped
// first when the terminal is too narrow; prio 0 columns are never dropped.
type column struct {
	title string
	width int // fixed width, or minimum width for the flex column
	flex  bool
	right bool
	prio  int
	sort  string // sort key this column represents ("" = not sortable)
	grow  bool   // receives surplus width once the flex column is wide enough
}

// row is one rendered line of a table.
type row struct {
	key   string
	cells []string
	group bool
	ref   any
}

// listState is the per-tab cursor/scroll/sort state.
type listState struct {
	cursor int
	offset int
	selKey string
	sortBy string
	desc   bool
}

type tableLayout struct {
	idx    []int // visible column indices
	widths []int
}

func layoutColumns(cols []column, total int) tableLayout {
	active := make([]int, len(cols))
	for i := range cols {
		active[i] = i
	}
	avail := total - 2 // one cell margin on each side
	for {
		sum := 0
		for _, i := range active {
			sum += cols[i].width
		}
		sum += len(active) - 1
		if sum <= avail {
			break
		}
		drop, best := -1, 0
		for k, i := range active {
			if cols[i].prio > best {
				best, drop = cols[i].prio, k
			}
		}
		if drop < 0 {
			break
		}
		active = append(active[:drop:drop], active[drop+1:]...)
	}
	l := tableLayout{idx: active, widths: make([]int, len(active))}
	sum := len(active) - 1
	flex := -1
	for k, i := range active {
		l.widths[k] = cols[i].width
		sum += cols[i].width
		if cols[i].flex {
			flex = k
		}
	}
	if flex >= 0 && avail > sum {
		extra := avail - sum
		// Don't let the name column grow absurdly wide on big terminals;
		// hand the surplus to a "grow" column (or the last one).
		const maxFlex = 40
		sink := -1
		for k, i := range active {
			if cols[i].grow {
				sink = k
			}
		}
		grow := extra
		if room := maxFlex - l.widths[flex]; sink >= 0 && grow > room {
			grow = room
		}
		if grow < 0 {
			grow = 0
		}
		l.widths[flex] += grow
		rest := extra - grow
		if rest > 0 && sink >= 0 {
			// cap the grow column too, then spill the rest into the last column
			give := rest
			if sink != len(l.widths)-1 && give > 24 {
				give = 24
			}
			l.widths[sink] += give
			l.widths[len(l.widths)-1] += rest - give
		} else {
			l.widths[flex] += rest
		}
	}
	return l
}

func (l tableLayout) render(cells []string, cols []column) string {
	var b strings.Builder
	b.WriteByte(' ')
	for k, i := range l.idx {
		if k > 0 {
			b.WriteByte(' ')
		}
		var s string
		if i < len(cells) {
			s = cells[i]
		}
		b.WriteString(fit(s, l.widths[k], cols[i].right))
	}
	b.WriteByte(' ')
	return b.String()
}

func renderHeader(cols []column, l tableLayout, st *listState, width int) string {
	cells := make([]string, len(cols))
	for i, c := range cols {
		t := c.title
		if c.sort != "" && c.sort == st.sortBy {
			if st.desc {
				t += "▼"
			} else {
				t += "▲"
			}
		}
		cells[i] = t
	}
	return sColHeader.Render(fit(l.render(cells, cols), width, false))
}

func renderRow(r row, cols []column, l tableLayout, selected bool, width int) string {
	line := l.render(r.cells, cols)
	if selected {
		return sSelected.Render(fit(ansi.Strip(line), width, false))
	}
	return fit(line, width, false)
}

// clampCursor keeps cursor/offset valid and restores selection by key.
func (st *listState) sync(rows []row, height int) {
	if st.selKey != "" {
		for i, r := range rows {
			if r.key == st.selKey {
				st.cursor = i
				break
			}
		}
	}
	if st.cursor >= len(rows) {
		st.cursor = len(rows) - 1
	}
	if st.cursor < 0 {
		st.cursor = 0
	}
	if len(rows) > 0 {
		st.selKey = rows[st.cursor].key
	}
	st.scroll(len(rows), height)
}

func (st *listState) scroll(n, height int) {
	if height < 1 {
		height = 1
	}
	if st.cursor < st.offset {
		st.offset = st.cursor
	}
	if st.cursor >= st.offset+height {
		st.offset = st.cursor - height + 1
	}
	if st.offset > n-height {
		st.offset = n - height
	}
	if st.offset < 0 {
		st.offset = 0
	}
}

func (st *listState) move(rows []row, delta, height int) {
	if len(rows) == 0 {
		return
	}
	st.cursor += delta
	if st.cursor < 0 {
		st.cursor = 0
	}
	if st.cursor >= len(rows) {
		st.cursor = len(rows) - 1
	}
	st.selKey = rows[st.cursor].key
	st.scroll(len(rows), height)
}
