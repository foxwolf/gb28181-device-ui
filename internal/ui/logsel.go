package ui

import (
	"image"
	"image/color"
	"io"
	"strings"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
	"golang.org/x/image/math/fixed"

	"gb28181-device-ui/internal/logview"
)

type logPos struct {
	line int
	col  int // 该行文本中的 rune 下标
}

func (a logPos) less(b logPos) bool {
	if a.line != b.line {
		return a.line < b.line
	}
	return a.col < b.col
}

func orderPos(a, b logPos) (logPos, logPos) {
	if b.less(a) {
		return b, a
	}
	return a, b
}

type lineHit struct {
	idx     int
	y, h, w int
}

func (u *UI) layoutLogText(gtx C) D {
	// 先根据上一帧行位置处理指针，本帧绘制就能跟上拖动。
	u.processLogPointer(gtx)
	u.processLogKeys(gtx)

	lines := u.logs.Lines()
	if u.stickBottom {
		u.logList.Position.BeforeEnd = false
	}

	n := len(lines)
	heights := make([]int, n)
	dims := u.logList.Layout(gtx, max(n, 1), func(gtx C, i int) D {
		if n == 0 {
			l := material.Label(u.th, unit.Sp(12), "暂无日志")
			l.Color = rgb(0x7A869A)
			return l.Layout(gtx)
		}
		d := u.layoutLogLine(gtx, i, lines[i])
		if i >= 0 && i < n {
			heights[i] = d.Size.Y
		}
		return d
	})

	u.logHits = u.logHits[:0]
	u.logWidth = gtx.Constraints.Max.X
	y := -u.logList.Position.Offset
	if n > 0 {
		for i := 0; i < u.logList.Position.Count; i++ {
			idx := u.logList.Position.First + i
			if idx < 0 || idx >= n {
				continue
			}
			h := heights[idx]
			if h == 0 {
				h = gtx.Sp(unit.Sp(16))
			}
			u.logHits = append(u.logHits, lineHit{idx: idx, y: y, h: h, w: u.logWidth})
			y += h
		}
	}

	if u.logDragging {
		u.stickBottom = false
	} else if !u.logFocused || !u.hasLogSelection() {
		u.stickBottom = !u.logList.Position.BeforeEnd
	}

	defer pointer.PassOp{}.Push(gtx.Ops).Pop()
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	pointer.CursorText.Add(gtx.Ops)
	event.Op(gtx.Ops, &u.logPtr)
	return dims
}

func (u *UI) layoutLogLine(gtx C, i int, line logview.Line) D {
	start, end := orderPos(u.logStart, u.logEnd)
	maxW := gtx.Constraints.Max.X
	if u.hasLogSelection() && i >= start.line && i <= end.line {
		cs, ce := 0, utf8.RuneCountInString(line.Text)
		if i == start.line {
			cs = start.col
		}
		if i == end.line {
			ce = end.col
		}
		for _, r := range u.selectionRects(gtx, line.Text, cs, ce, maxW) {
			func() {
				defer clip.Rect(r).Push(gtx.Ops).Pop()
				paint.Fill(gtx.Ops, color.NRGBA{R: 0x90, G: 0xCA, B: 0xF9, A: 0xFF})
			}()
		}
	}
	l := material.Label(u.th, unit.Sp(12), line.Text)
	l.Color = levelColor(line.Level)
	l.MaxLines = 0
	return layout.Inset{Bottom: unit.Dp(2)}.Layout(gtx, l.Layout)
}

func (u *UI) processWinPointer(gtx C) {
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target: &u.winPtr,
			Kinds:  pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel,
		})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		if e.Kind == pointer.Press || e.Kind == pointer.Release {
			if e.Kind == pointer.Press && e.Source == pointer.Mouse && e.Buttons != pointer.ButtonPrimary {
				continue
			}
			u.winPress = e.Position.Round()
			u.haveWinPress = true
		}
	}
}

func (u *UI) processLogPointer(gtx C) {
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target: &u.logPtr,
			Kinds:  pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel,
		})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		pt := e.Position.Round()
		switch e.Kind {
		case pointer.Press:
			if e.Source == pointer.Mouse && e.Buttons != pointer.ButtonPrimary {
				continue
			}
			u.logPressLocal = pt
			if u.haveWinPress {
				u.logOrigin = u.winPress.Sub(pt)
				u.haveLogOrigin = true
			} else {
				u.haveLogOrigin = false
			}
			p := u.posAt(gtx, pt)
			u.logStart, u.logEnd = p, p
			u.logDragging = true
			u.logFocused = true
			gtx.Execute(key.FocusCmd{Tag: &u.logPtr})
			gtx.Execute(pointer.GrabCmd{Tag: &u.logPtr, ID: e.PointerID})
		case pointer.Drag, pointer.Release:
			if !u.logDragging {
				continue
			}
			if e.Kind == pointer.Release && !nearOrigin(pt) {
				u.logEnd = u.posAt(gtx, pt)
			} else if lp, ok := u.cursorLogPoint(); ok {
				u.logEnd = u.posAt(gtx, lp)
			} else if e.Kind == pointer.Release || !nearOrigin(pt) {
				u.logEnd = u.posAt(gtx, pt)
			}
			if e.Kind == pointer.Release {
				u.logDragging = false
			}
		case pointer.Cancel:
			u.logDragging = false
		}
	}
	if u.logDragging {
		if lp, ok := u.cursorLogPoint(); ok {
			u.logEnd = u.posAt(gtx, lp)
		}
		gtx.Execute(op.InvalidateCmd{})
	}
}

// cursorLogPoint 用窗口客户区光标减去日志视口原点，避免“按下时记下的屏幕坐标已经落后一截”。
func (u *UI) cursorLogPoint() (image.Point, bool) {
	if !u.haveLogOrigin {
		return image.Point{}, false
	}
	c, ok := clientCursor(u.hwnd)
	if !ok {
		return image.Point{}, false
	}
	if cs, ok := windowClientSize(u.hwnd); ok && cs.X > 0 && cs.Y > 0 && u.frameSize.X > 0 && u.frameSize.Y > 0 && cs != u.frameSize {
		c.X = c.X * u.frameSize.X / cs.X
		c.Y = c.Y * u.frameSize.Y / cs.Y
	}
	return c.Sub(u.logOrigin), true
}

func nearOrigin(p image.Point) bool {
	return p.X < 8 && p.Y < 8
}

func (u *UI) processLogKeys(gtx C) {
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: &u.logPtr},
			key.Filter{Focus: &u.logPtr, Name: "C", Required: key.ModShortcut},
			key.Filter{Focus: &u.logPtr, Name: "A", Required: key.ModShortcut},
		)
		if !ok {
			break
		}
		switch ev := ev.(type) {
		case key.FocusEvent:
			u.logFocused = ev.Focus
		case key.Event:
			if ev.State != key.Press {
				continue
			}
			switch ev.Name {
			case "C":
				if text := u.selectedLogText(); text != "" {
					gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
					u.logCopiedAt = gtx.Now
					if u.win != nil {
						u.win.Invalidate()
					}
				}
			case "A":
				lines := u.logs.Lines()
				if len(lines) == 0 {
					continue
				}
				last := lines[len(lines)-1]
				u.logStart = logPos{line: 0, col: 0}
				u.logEnd = logPos{line: len(lines) - 1, col: utf8.RuneCountInString(last.Text)}
			}
		}
	}
}

func (u *UI) posAt(gtx C, pt image.Point) logPos {
	lines := u.logs.Lines()
	if len(lines) == 0 || len(u.logHits) == 0 {
		return logPos{}
	}
	if pt.Y < u.logHits[0].y {
		return logPos{line: u.logHits[0].idx, col: 0}
	}
	last := u.logHits[len(u.logHits)-1]
	if pt.Y >= last.y+last.h {
		s := lines[last.idx].Text
		return logPos{line: last.idx, col: utf8.RuneCountInString(s)}
	}
	for _, hit := range u.logHits {
		if pt.Y < hit.y+hit.h {
			local := image.Pt(pt.X, pt.Y-hit.y)
			col := u.runeAt(gtx, lines[hit.idx].Text, local, hit.w)
			return logPos{line: hit.idx, col: col}
		}
	}
	s := lines[last.idx].Text
	return logPos{line: last.idx, col: utf8.RuneCountInString(s)}
}

func (u *UI) runeAt(gtx C, s string, local image.Point, maxW int) int {
	if s == "" || maxW <= 0 {
		return 0
	}
	glyphs := u.shapeLog(gtx, s, maxW)
	if len(glyphs) == 0 {
		return 0
	}
	// 先按行带选中（支持折行），再只根据 X 定位，避免 Y 稍有偏差就落到第 0 列。
	row := glyphs[0]
	bestRow := rowDistY(local.Y, glyphs[0])
	for _, g := range glyphs[1:] {
		if d := rowDistY(local.Y, g); d < bestRow {
			row, bestRow = g, d
		}
	}
	best := row.at
	for _, g := range glyphs {
		if rowDistY(local.Y, g) != bestRow && g.y != row.y {
			continue
		}
		if local.X < g.x {
			return g.at
		}
		if local.X < g.x+g.adv {
			if local.X-g.x < g.x+g.adv-local.X {
				return g.at
			}
			return g.after
		}
		best = g.after
	}
	return best
}

func rowDistY(y int, g logGlyph) int {
	top, bot := g.y-g.ascent, g.y+g.descent
	if y >= top && y < bot {
		return 0
	}
	if y < top {
		return top - y
	}
	return y - bot + 1
}

func (u *UI) selectionRects(gtx C, s string, startCol, endCol, maxW int) []image.Rectangle {
	if startCol > endCol {
		startCol, endCol = endCol, startCol
	}
	if startCol == endCol || s == "" {
		return nil
	}
	glyphs := u.shapeLog(gtx, s, maxW)
	var out []image.Rectangle
	for _, g := range glyphs {
		if g.after <= startCol || g.at >= endCol {
			continue
		}
		x0 := g.x
		x1 := g.x + g.adv
		if g.at < startCol {
			x0 = g.x + g.adv*(startCol-g.at)/max(g.after-g.at, 1)
		}
		if g.after > endCol {
			x1 = g.x + g.adv*(endCol-g.at)/max(g.after-g.at, 1)
		}
		r := image.Rect(x0, g.y-g.ascent, x1, g.y+g.descent)
		if r.Dx() < 1 {
			r.Max.X = r.Min.X + 1
		}
		out = append(out, r)
	}
	return out
}

type logGlyph struct {
	at, after          int
	x, adv             int
	y, ascent, descent int
}

func (u *UI) shapeLog(gtx C, s string, maxW int) []logGlyph {
	px := gtx.Sp(unit.Sp(12))
	u.th.Shaper.LayoutString(text.Parameters{
		Font:     font.Font{Typeface: u.th.Face},
		PxPerEm:  fixed.I(px),
		MaxWidth: maxW,
		MinWidth: 0,
		Locale:   gtx.Locale,
	}, s)
	var out []logGlyph
	at := 0
	for {
		g, ok := u.th.Shaper.NextGlyph()
		if !ok {
			break
		}
		runes := int(g.Runes)
		if g.Flags&text.FlagClusterBreak == 0 {
			runes = 0
		}
		after := at + runes
		if runes > 0 || g.Advance > 0 {
			out = append(out, logGlyph{
				at:      at,
				after:   after,
				x:       g.X.Floor(),
				adv:     g.Advance.Ceil(),
				y:       int(g.Y),
				ascent:  g.Ascent.Ceil(),
				descent: g.Descent.Ceil(),
			})
		}
		if runes > 0 {
			at = after
		}
	}
	return out
}

func (u *UI) hasLogSelection() bool {
	return u.logStart != u.logEnd
}

func (u *UI) selectedLogText() string {
	if !u.hasLogSelection() {
		return ""
	}
	lines := u.logs.Lines()
	if len(lines) == 0 {
		return ""
	}
	a, b := orderPos(u.logStart, u.logEnd)
	if a.line < 0 {
		a.line = 0
	}
	if b.line >= len(lines) {
		b.line = len(lines) - 1
	}
	var bld strings.Builder
	for i := a.line; i <= b.line; i++ {
		runes := []rune(lines[i].Text)
		cs, ce := 0, len(runes)
		if i == a.line {
			cs = clampIdx(a.col, len(runes))
		}
		if i == b.line {
			ce = clampIdx(b.col, len(runes))
		}
		if cs > ce {
			cs, ce = ce, cs
		}
		if i > a.line {
			bld.WriteByte('\n')
		}
		bld.WriteString(string(runes[cs:ce]))
	}
	return bld.String()
}

func clampIdx(i, n int) int {
	if i < 0 {
		return 0
	}
	if i > n {
		return n
	}
	return i
}
