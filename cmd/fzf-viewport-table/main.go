package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type snapshot struct {
	Type          string         `json:"type"`
	Revision      int64          `json:"revision"`
	Query         string         `json:"query"`
	QueryCursor   int            `json:"queryCursor"`
	Prompt        string         `json:"prompt"`
	InputVisible  bool           `json:"inputVisible"`
	HeaderVisible bool           `json:"headerVisible"`
	Reading       bool           `json:"reading"`
	Progress      int            `json:"progress"`
	TotalCount    int            `json:"totalCount"`
	MatchCount    int            `json:"matchCount"`
	SelectedCount int            `json:"selectedCount"`
	CurrentIndex  int            `json:"currentIndex"`
	Offset        int            `json:"offset"`
	PageSize      int            `json:"pageSize"`
	Sort          bool           `json:"sort"`
	Raw           bool           `json:"raw"`
	Items         []viewportItem `json:"items"`
}

type viewportItem struct {
	ID        int    `json:"id"`
	Key       string `json:"key"`
	Text      string `json:"text"`
	Display   string `json:"display"`
	Positions []int  `json:"positions"`
	Matched   bool   `json:"matched"`
	Current   bool   `json:"current"`
	Selected  bool   `json:"selected"`
}

type outputEvent struct {
	Type    string   `json:"type"`
	Query   string   `json:"query"`
	Pressed string   `json:"pressed"`
	Items   []string `json:"items"`
}

type options struct {
	listen    string
	delimiter string
	columns   []string
	useRaw    bool
	apiKey    string
}

type app struct {
	opts      options
	renderOut *os.File
	ttyIn     *os.File
	client    *listenClient

	mu         sync.Mutex
	snapshot   snapshot
	hasSnap    bool
	lastOutput *outputEvent
}

type listenClient struct {
	addr   string
	apiKey string
	http   *http.Client
	url    string
}

type row struct {
	marker string
	index  int
	cells  []string
	render []string
}

func main() {
	opts := parseFlags()

	renderOut := os.Stdout
	ttyIn, err := os.Open("/dev/tty")
	if err != nil && opts.listen != "" {
		exitf("open /dev/tty: %v", err)
	}
	if ttyIn != nil {
		defer ttyIn.Close()
	}

	app := &app{
		opts:      opts,
		renderOut: renderOut,
		ttyIn:     ttyIn,
	}
	if opts.listen != "" {
		client, err := newListenClient(opts.listen, opts.apiKey)
		if err != nil {
			exitf("%v", err)
		}
		app.client = client
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	if app.client != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.readKeys(ctx)
		}()
	}

	if err := app.readStream(ctx, os.Stdin); err != nil && ctx.Err() == nil {
		exitf("%v", err)
	}
	stop()
	wg.Wait()
}

func parseFlags() options {
	var cols string
	opts := options{}

	flag.StringVar(&opts.listen, "listen", "", "fzf --listen address (port, host:port, or unix socket path)")
	flag.StringVar(&opts.delimiter, "delimiter", "\t", "column delimiter")
	flag.StringVar(&cols, "columns", "", "comma-separated column names")
	flag.BoolVar(&opts.useRaw, "raw", false, "use raw item text instead of display text")
	flag.StringVar(&opts.apiKey, "api-key", "", "optional API key for fzf --listen")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: bin/fzf-viewport-table [options] < stream.jsonl\n\n")
		fmt.Fprintln(os.Stderr, "Render fzf viewport snapshots as an aligned table and optionally forward tty input to fzf --listen.")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  tail -f /tmp/fzf-vp.jsonl | bin/fzf-viewport-table --columns 'ID,Project,Owner'")
		fmt.Fprintln(os.Stderr, "  tail -n +2 test/viewport-sheet.tsv \\")
		fmt.Fprintln(os.Stderr, "    | bin/fzf --headless --listen 6266 --viewport-stream=- \\")
		fmt.Fprintln(os.Stderr, "    | bin/fzf-viewport-table --listen 6266 --delimiter='\\t' \\")
		fmt.Fprintln(os.Stderr, "        --columns 'ID,Project,Owner,Region,Status,Priority,ARR,Renewal,Notes'")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Keys:")
		fmt.Fprintln(os.Stderr, "  text inserts into the query, arrows move, Enter accepts, Ctrl-S toggles, Ctrl-U clears, Ctrl-C aborts")
		fmt.Fprintln(os.Stderr)
		flag.PrintDefaults()
	}
	flag.Parse()

	if cols != "" {
		for _, col := range strings.Split(cols, ",") {
			opts.columns = append(opts.columns, strings.TrimSpace(col))
		}
	}
	opts.delimiter = decodeEscapes(opts.delimiter)
	return opts
}

func newListenClient(address string, apiKey string) (*listenClient, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("--listen address required")
	}

	client := &listenClient{addr: address, apiKey: apiKey}
	if strings.HasSuffix(address, ".sock") {
		transport := &http.Transport{
			DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", address)
			},
		}
		client.http = &http.Client{Transport: transport}
		client.url = "http://unix/"
		return client, nil
	}

	host := address
	if !strings.Contains(host, ":") {
		host = "localhost:" + host
	}
	client.http = &http.Client{Timeout: 2 * time.Second}
	client.url = "http://" + host + "/"
	return client, nil
}

func (c *listenClient) post(action string) error {
	req, err := http.NewRequest(http.MethodPost, c.url, strings.NewReader(action))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("listen post %q failed: %s", action, msg)
	}
	return nil
}

func (a *app) readStream(ctx context.Context, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &kind); err != nil {
			return fmt.Errorf("decode event type: %w", err)
		}
		switch kind.Type {
		case "snapshot":
			var snap snapshot
			if err := json.Unmarshal([]byte(line), &snap); err != nil {
				return fmt.Errorf("decode snapshot: %w", err)
			}
			a.mu.Lock()
			a.snapshot = snap
			a.hasSnap = true
			a.mu.Unlock()
			a.render()
		case "output":
			var out outputEvent
			if err := json.Unmarshal([]byte(line), &out); err != nil {
				return fmt.Errorf("decode output event: %w", err)
			}
			a.mu.Lock()
			a.lastOutput = &out
			a.mu.Unlock()
			a.render()
		}
	}
	return scanner.Err()
}

func (a *app) readKeys(ctx context.Context) {
	oldState, err := captureTTYState()
	if err != nil {
		return
	}
	if err := setTTYInputMode(); err != nil {
		return
	}
	defer restoreTTYState(oldState)

	reader := bufio.NewReader(a.ttyIn)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		action, quit, err := readAction(reader)
		if err != nil {
			return
		}
		if action != "" {
			if err := a.client.post(action); err != nil {
				a.renderStatus("listen error: " + err.Error())
			}
		}
		if quit {
			return
		}
	}
}

func readAction(reader *bufio.Reader) (string, bool, error) {
	b, err := reader.ReadByte()
	if err != nil {
		return "", false, err
	}

	switch b {
	case 0x03:
		return "abort", true, nil
	case 0x13:
		return "toggle", false, nil
	case 0x15:
		return "clear-query", false, nil
	case 0x01:
		return "beginning-of-line", false, nil
	case 0x05:
		return "end-of-line", false, nil
	case 0x7f, 0x08:
		return "backward-delete-char", false, nil
	case '\r', '\n':
		return "accept", true, nil
	case '\t':
		return "down", false, nil
	case 0x1b:
		return readEscapeAction(reader)
	default:
		if b >= 0x20 {
			text, err := readTextRune(reader, b)
			if err != nil {
				return "", false, err
			}
			return "put" + wrapActionArg(text), false, nil
		}
	}
	return "", false, nil
}

func readEscapeAction(reader *bufio.Reader) (string, bool, error) {
	next, err := reader.ReadByte()
	if err != nil {
		return "", false, err
	}
	if next != '[' && next != 'O' {
		return "", false, nil
	}

	third, err := reader.ReadByte()
	if err != nil {
		return "", false, err
	}
	switch third {
	case 'A':
		return "up", false, nil
	case 'B':
		return "down", false, nil
	case 'C':
		return "forward-char", false, nil
	case 'D':
		return "backward-char", false, nil
	case 'H':
		return "beginning-of-line", false, nil
	case 'F':
		return "end-of-line", false, nil
	case 'Z':
		return "up", false, nil
	case '3':
		if tail, err := reader.ReadByte(); err == nil && tail == '~' {
			return "delete-char", false, nil
		}
	case '5':
		if tail, err := reader.ReadByte(); err == nil && tail == '~' {
			return "page-up", false, nil
		}
	case '6':
		if tail, err := reader.ReadByte(); err == nil && tail == '~' {
			return "page-down", false, nil
		}
	case '1', '7':
		if tail, err := reader.ReadByte(); err == nil && tail == '~' {
			return "beginning-of-line", false, nil
		}
	case '4', '8':
		if tail, err := reader.ReadByte(); err == nil && tail == '~' {
			return "end-of-line", false, nil
		}
	}
	return "", false, nil
}

func wrapActionArg(value string) string {
	delims := []struct{ open, close string }{
		{"(", ")"},
		{"[", "]"},
		{"{", "}"},
		{"<", ">"},
		{"/", "/"},
		{"|", "|"},
		{";", ";"},
		{"!", "!"},
		{"@", "@"},
		{"#", "#"},
		{"$", "$"},
		{"%", "%"},
		{"^", "^"},
		{"&", "&"},
		{"*", "*"},
		{"~", "~"},
	}
	for _, delim := range delims {
		if !strings.Contains(value, delim.open) && !strings.Contains(value, delim.close) {
			return delim.open + value + delim.close
		}
	}
	return "(" + strings.ReplaceAll(value, ")", "") + ")"
}

func (a *app) renderStatus(message string) {
	if stdoutIsTTY() {
		fmt.Fprint(a.renderOut, "\x1b[H\x1b[2J")
	}
	fmt.Fprintln(a.renderOut, "fzf viewport table")
	fmt.Fprintln(a.renderOut)
	fmt.Fprintln(a.renderOut, message)
}

func (a *app) render() {
	a.mu.Lock()
	snap := a.snapshot
	hasSnap := a.hasSnap
	lastOutput := a.lastOutput
	a.mu.Unlock()
	if !hasSnap {
		return
	}

	var buf bytes.Buffer
	if stdoutIsTTY() {
		buf.WriteString("\x1b[H\x1b[2J")
	}
	buf.WriteString("fzf viewport table\n\n")
	buf.WriteString(renderQuery(snap))
	buf.WriteByte('\n')
	buf.WriteString(metadataLine(snap))
	buf.WriteByte('\n')
	buf.WriteString(keyLegend(a.client != nil))
	buf.WriteString("\n\n")

	rows := a.buildRows(snap)
	if len(rows) == 0 {
		buf.WriteString("(no visible rows)\n")
	} else {
		columnCount := 0
		for _, row := range rows {
			if len(row.cells) > columnCount {
				columnCount = len(row.cells)
			}
		}
		if len(a.opts.columns) > columnCount {
			columnCount = len(a.opts.columns)
		}
		headers := headerNames(a.opts.columns, columnCount)
		widths := columnWidths(rows, headers, columnCount)
		rowWidth := len(strconv.Itoa(maxRowIndex(rows)))

		buf.WriteString(tableHeader(headers, widths, rowWidth))
		buf.WriteByte('\n')
		buf.WriteString(tableSeparator(widths, rowWidth))
		buf.WriteByte('\n')
		for _, row := range rows {
			buf.WriteString(tableRow(row, widths, rowWidth, columnCount))
			buf.WriteByte('\n')
		}
	}

	if lastOutput != nil {
		buf.WriteByte('\n')
		buf.WriteString(outputLine(*lastOutput))
		buf.WriteByte('\n')
	}

	_, _ = a.renderOut.Write(buf.Bytes())
}

func renderQuery(s snapshot) string {
	queryRunes := []rune(s.Query)
	cursor := s.QueryCursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(queryRunes) {
		cursor = len(queryRunes)
	}
	queryRunes = append(queryRunes[:cursor], append([]rune{'|'}, queryRunes[cursor:]...)...)
	return fmt.Sprintf("query: %s%s", s.Prompt, string(queryRunes))
}

func metadataLine(s snapshot) string {
	parts := []string{
		fmt.Sprintf("rev=%d", s.Revision),
		fmt.Sprintf("matches=%d", s.MatchCount),
		fmt.Sprintf("selected=%d", s.SelectedCount),
		fmt.Sprintf("offset=%d", s.Offset),
		fmt.Sprintf("current=%d", s.CurrentIndex),
		fmt.Sprintf("page=%d", s.PageSize),
		fmt.Sprintf("reading=%t", s.Reading),
		fmt.Sprintf("progress=%d%%", s.Progress),
	}
	return strings.Join(parts, "  ")
}

func keyLegend(interactive bool) string {
	if !interactive {
		return "view-only mode"
	}
	return "keys: type=query  arrows=move  Enter=accept  Ctrl-S=toggle  Ctrl-U=clear  Ctrl-C=abort"
}

func (a *app) buildRows(s snapshot) []row {
	rows := make([]row, 0, len(s.Items))
	enableANSI := stdoutIsTTY()
	for index, item := range s.Items {
		source := item.Display
		if a.opts.useRaw || source == "" {
			source = item.Text
		}
		cells := strings.Split(source, a.opts.delimiter)
		render := decorateCells(a.opts.delimiter, cells, item.Positions, enableANSI)
		rows = append(rows, row{
			marker: marker(item),
			index:  s.Offset + index,
			cells:  cells,
			render: render,
		})
	}
	return rows
}

func marker(item viewportItem) string {
	current := ' '
	selected := ' '
	if item.Current {
		current = '>'
	}
	if item.Selected {
		selected = '*'
	}
	return string([]rune{current, selected})
}

func decorateCells(delimiter string, cells []string, positions []int, enableANSI bool) []string {
	if !enableANSI {
		return append([]string(nil), cells...)
	}
	positionSet := make(map[int]struct{}, len(positions))
	for _, pos := range positions {
		positionSet[pos] = struct{}{}
	}
	delimiterWidth := len([]rune(delimiter))
	offset := 0
	rendered := make([]string, 0, len(cells))
	for _, cell := range cells {
		var b strings.Builder
		for idx, r := range []rune(cell) {
			if _, ok := positionSet[offset+idx]; ok {
				b.WriteString("\x1b[1;4m")
				b.WriteRune(r)
				b.WriteString("\x1b[0m")
			} else {
				b.WriteRune(r)
			}
		}
		rendered = append(rendered, b.String())
		offset += len([]rune(cell)) + delimiterWidth
	}
	return rendered
}

func headerNames(columns []string, columnCount int) []string {
	headers := make([]string, columnCount)
	for i := 0; i < columnCount; i++ {
		if i < len(columns) && columns[i] != "" {
			headers[i] = columns[i]
		} else {
			headers[i] = fmt.Sprintf("col%d", i+1)
		}
	}
	return headers
}

func columnWidths(rows []row, headers []string, columnCount int) []int {
	widths := make([]int, columnCount)
	for i := 0; i < columnCount; i++ {
		widths[i] = len([]rune(headers[i]))
		for _, row := range rows {
			width := 0
			if i < len(row.cells) {
				width = len([]rune(row.cells[i]))
			}
			if width > widths[i] {
				widths[i] = width
			}
		}
	}
	return widths
}

func maxRowIndex(rows []row) int {
	maxIndex := 0
	for _, row := range rows {
		if row.index > maxIndex {
			maxIndex = row.index
		}
	}
	return maxIndex
}

func tableHeader(headers []string, widths []int, rowWidth int) string {
	parts := make([]string, len(headers))
	for i := range headers {
		parts[i] = pad(headers[i], len([]rune(headers[i])), widths[i])
	}
	return fmt.Sprintf("st %*s | %s", rowWidth, "#", strings.Join(parts, " | "))
}

func tableSeparator(widths []int, rowWidth int) string {
	parts := make([]string, len(widths))
	for i, width := range widths {
		parts[i] = strings.Repeat("-", width)
	}
	return fmt.Sprintf("-- %s-+-%s", strings.Repeat("-", rowWidth), strings.Join(parts, "-+-"))
}

func tableRow(row row, widths []int, rowWidth int, columnCount int) string {
	parts := make([]string, columnCount)
	for i := 0; i < columnCount; i++ {
		rendered := ""
		plainWidth := 0
		if i < len(row.render) {
			rendered = row.render[i]
		}
		if i < len(row.cells) {
			plainWidth = len([]rune(row.cells[i]))
		}
		parts[i] = pad(rendered, plainWidth, widths[i])
	}
	return fmt.Sprintf("%2s %*d | %s", row.marker, rowWidth, row.index, strings.Join(parts, " | "))
}

func pad(rendered string, plainWidth int, targetWidth int) string {
	if plainWidth >= targetWidth {
		return rendered
	}
	return rendered + strings.Repeat(" ", targetWidth-plainWidth)
}

func outputLine(out outputEvent) string {
	parts := []string{"output:"}
	if out.Query != "" {
		parts = append(parts, fmt.Sprintf("query=%q", out.Query))
	}
	if out.Pressed != "" {
		parts = append(parts, fmt.Sprintf("pressed=%q", out.Pressed))
	}
	if len(out.Items) > 0 {
		quoted := make([]string, len(out.Items))
		for i, item := range out.Items {
			quoted[i] = fmt.Sprintf("%q", item)
		}
		parts = append(parts, "items="+strings.Join(quoted, ", "))
	}
	return strings.Join(parts, " ")
}

func decodeEscapes(str string) string {
	replacer := strings.NewReplacer(`\t`, "\t", `\n`, "\n", `\r`, "\r", `\\`, `\`)
	return replacer.Replace(str)
}

func readTextRune(reader *bufio.Reader, first byte) (string, error) {
	if first < utf8.RuneSelf {
		return string([]byte{first}), nil
	}
	buf := []byte{first}
	for !utf8.FullRune(buf) {
		next, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		buf = append(buf, next)
	}
	r, _ := utf8.DecodeRune(buf)
	return string(r), nil
}

func stdoutIsTTY() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func captureTTYState() (string, error) {
	out, err := exec.Command("sh", "-c", "stty -g < /dev/tty").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func setTTYInputMode() error {
	cmd := exec.Command("sh", "-c", "stty -echo -icanon -isig min 1 time 0 < /dev/tty")
	return cmd.Run()
}

func restoreTTYState(state string) {
	if state == "" {
		return
	}
	_ = exec.Command("sh", "-c", "stty "+state+" < /dev/tty").Run()
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
