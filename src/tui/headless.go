package tui

import (
	"os"
	"strconv"
	"sync"
)

type HeadlessRenderer struct {
	theme      *ColorTheme
	size       TermSize
	cancelChan chan struct{}
	mutex      sync.Mutex
	showCursor bool
}

type HeadlessWindow struct {
	top      int
	left     int
	width    int
	height   int
	x        int
	y        int
	wrapSign string
}

func NewHeadlessRenderer(theme *ColorTheme, pageSize int) Renderer {
	columns := 80
	if env := os.Getenv("COLUMNS"); len(env) > 0 {
		if parsed, err := strconv.Atoi(env); err == nil && parsed > 0 {
			columns = parsed
		}
	}
	lines := pageSize + 16
	if lines < 32 {
		lines = 32
	}
	return &HeadlessRenderer{
		theme:      theme,
		size:       TermSize{Lines: lines, Columns: columns},
		cancelChan: make(chan struct{}),
		showCursor: true,
	}
}

func (r *HeadlessRenderer) DefaultTheme() *ColorTheme          { return r.theme }
func (r *HeadlessRenderer) Init() error                        { return nil }
func (r *HeadlessRenderer) Resize(maxHeightFunc func(int) int) {}
func (r *HeadlessRenderer) Pause(clear bool)                   {}
func (r *HeadlessRenderer) Resume(clear bool, sigcont bool)    {}
func (r *HeadlessRenderer) Clear()                             {}
func (r *HeadlessRenderer) RefreshWindows(windows []Window)    {}
func (r *HeadlessRenderer) Refresh()                           {}
func (r *HeadlessRenderer) Close()                             { r.CancelGetChar() }
func (r *HeadlessRenderer) PassThrough(string)                 {}
func (r *HeadlessRenderer) NeedScrollbarRedraw() bool          { return false }
func (r *HeadlessRenderer) ShouldEmitResizeEvent() bool        { return false }
func (r *HeadlessRenderer) Bell()                              {}
func (r *HeadlessRenderer) HideCursor()                        { r.showCursor = false }
func (r *HeadlessRenderer) ShowCursor()                        { r.showCursor = true }
func (r *HeadlessRenderer) Top() int                           { return 0 }
func (r *HeadlessRenderer) MaxX() int                          { return r.size.Columns }
func (r *HeadlessRenderer) MaxY() int                          { return r.size.Lines }
func (r *HeadlessRenderer) Size() TermSize                     { return r.size }

func (r *HeadlessRenderer) GetChar(bool) Event {
	r.mutex.Lock()
	cancelChan := r.cancelChan
	r.mutex.Unlock()
	<-cancelChan
	return Invalid.AsEvent()
}

func (r *HeadlessRenderer) CancelGetChar() {
	r.mutex.Lock()
	close(r.cancelChan)
	r.cancelChan = make(chan struct{})
	r.mutex.Unlock()
}

func (r *HeadlessRenderer) NewWindow(top int, left int, width int, height int, windowType WindowType, borderStyle BorderStyle, erase bool) Window {
	return &HeadlessWindow{top: top, left: left, width: width, height: height}
}

func (w *HeadlessWindow) Top() int    { return w.top }
func (w *HeadlessWindow) Left() int   { return w.left }
func (w *HeadlessWindow) Width() int  { return w.width }
func (w *HeadlessWindow) Height() int { return w.height }

func (w *HeadlessWindow) DrawBorder()  {}
func (w *HeadlessWindow) DrawHBorder() {}
func (w *HeadlessWindow) Refresh()     {}
func (w *HeadlessWindow) FinishFill()  {}

func (w *HeadlessWindow) X() int { return w.x }
func (w *HeadlessWindow) Y() int { return w.y }

func (w *HeadlessWindow) EncloseX(x int) bool { return x >= w.left && x < w.left+w.width }
func (w *HeadlessWindow) EncloseY(y int) bool { return y >= w.top && y < w.top+w.height }
func (w *HeadlessWindow) Enclose(y int, x int) bool {
	return w.EncloseX(x) && w.EncloseY(y)
}

func (w *HeadlessWindow) Move(y int, x int) {
	w.y = y
	w.x = x
}

func (w *HeadlessWindow) MoveAndClear(y int, x int) {
	w.Move(y, x)
}

func (w *HeadlessWindow) Print(text string) {
	w.x += len([]rune(text))
}

func (w *HeadlessWindow) CPrint(color ColorPair, text string) {
	w.Print(text)
}

func (w *HeadlessWindow) Fill(text string) FillReturn {
	w.Print(text)
	return FillContinue
}

func (w *HeadlessWindow) CFill(fg Color, bg Color, ul Color, attr Attr, text string) FillReturn {
	w.Print(text)
	return FillContinue
}

func (w *HeadlessWindow) LinkBegin(uri string, params string) {}
func (w *HeadlessWindow) LinkEnd()                            {}
func (w *HeadlessWindow) Erase()                              {}
func (w *HeadlessWindow) EraseMaybe() bool                    { return true }

func (w *HeadlessWindow) SetWrapSign(sign string, width int) {
	w.wrapSign = sign
}
