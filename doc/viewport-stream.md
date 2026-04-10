# Viewport Stream

`fzf` can emit the current visible result list as newline-delimited JSON with
`--viewport-stream`. This is meant for alternate frontends, debugging, and
inspection of the session state that the built-in TUI is already maintaining.

The stream contains snapshot events with:

- query text and query cursor
- viewport offset and page size
- total/match/selection counts
- visible rows in order
- per-row flags such as current/selected
- match positions

It can also emit `output` events when fzf accepts or prints results.

## Modes

### Side-channel mode

Keep the normal fzf TUI and mirror viewport snapshots to a file or FIFO:

```sh
mkfifo /tmp/fzf-vp.fifo

cat /tmp/fzf-vp.fifo | bin/fzf-viewport-ui

tail -n +2 test/viewport-sheet.tsv \
  | bin/fzf --viewport-stream=/tmp/fzf-vp.fifo
```

This does **not** require `--headless`.

### Headless mode

Disable the built-in TUI and emit snapshots on stdout:

```sh
tail -n +2 test/viewport-sheet.tsv \
  | bin/fzf --headless --viewport-stream=- \
  | bin/fzf-viewport-ui
```

Headless mode is useful when the external frontend is the only renderer.

## Debug consumer

The repo includes a simple stream viewer:

```sh
bin/fzf-viewport-ui --help
```

This renders the latest snapshot in a readable text form and is useful for:

- verifying that the stream is active
- inspecting query and cursor state
- confirming visible rows and match positions

## Table frontend

The repo also includes a table-oriented frontend:

```sh
bin/fzf-viewport-table --help
```

It reads snapshots from stdin, splits each visible row into columns, and
renders the current viewport with widths computed from the visible rows.

Example:

```sh
mkfifo /tmp/fzf-vp.fifo

cat /tmp/fzf-vp.fifo \
  | bin/fzf-viewport-table \
      --delimiter='\t' \
      --columns 'ID,Project,Owner,Region,Status,Priority,ARR,Renewal,Notes'

tail -n +2 test/viewport-sheet.tsv \
  | bin/fzf --viewport-stream=/tmp/fzf-vp.fifo
```

### Driving fzf with `--listen`

If the external frontend should also send user input back to fzf, run fzf with
`--listen` and point the frontend at the same socket or port.

Unix socket example:

```sh
mkfifo /tmp/fzf-vp.fifo

cat /tmp/fzf-vp.fifo \
  | bin/fzf-viewport-table --listen /tmp/fzf.sock \
      --delimiter='\t' \
      --columns 'ID,Project,Owner,Region,Status,Priority,ARR,Renewal,Notes'

tail -n +2 test/viewport-sheet.tsv \
  | bin/fzf --listen /tmp/fzf.sock --viewport-stream=/tmp/fzf-vp.fifo
```

TCP example:

```sh
tail -n +2 test/viewport-sheet.tsv \
  | bin/fzf --headless --listen 6266 --viewport-stream=- \
  | bin/fzf-viewport-table --listen 6266 \
      --delimiter='\t' \
      --columns 'ID,Project,Owner,Region,Status,Priority,ARR,Renewal,Notes'
```

Current table frontend keys:

- printable text inserts into the query
- arrow keys move the cursor
- `Enter` accepts
- `Ctrl-S` toggles selection
- `Ctrl-U` clears the query
- `Ctrl-C` aborts

## Test data

For quick manual testing, the repo includes:

- `test/viewport-sheet.tsv`

It is intentionally uneven so column alignment issues are easy to spot.

## Notes

- `--viewport-stream=-` requires `--headless`
- a file or FIFO path works with either normal TUI mode or headless mode
- side-channel mode is the easiest way to compare the built-in TUI with an
  alternate frontend
- if you capture frontend output to a regular file, repeated snapshots will
  appear as repeated full renders; this is expected because the frontend
  redraws from complete snapshots
