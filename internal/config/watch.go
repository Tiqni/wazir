package config

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ResolvePath returns the config file Load(flagConfig) would read, and ok=false
// for an env-only run (no file to watch). When flagConfig is set it must exist.
func ResolvePath(flagConfig string) (path string, ok bool) {
	if flagConfig != "" {
		return flagConfig, fileExists(flagConfig)
	}
	if dir, name, found := defaultConfigFile(); found {
		return filepath.Join(dir, name), true
	}
	return "", false
}

// Watch reloads `path` whenever it changes, calling onReload(cfg) on a successful
// load+validate or onError(err) when the reloaded file is invalid. It watches the
// parent directory (so an editor's write-and-rename keeps the watch), debounces
// bursts, and returns when ctx is done. Logger-free by design.
//
// A polling fallback (every pollInterval) ensures events are not missed due to
// goroutine scheduling races or editor rename patterns. The poll compares mtime;
// changes detected by either mechanism are debounced before loading.
func Watch(ctx context.Context, path string, onReload func(Config), onError func(error)) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := w.Add(filepath.Dir(path)); err != nil {
		return err
	}
	base := filepath.Base(path)
	const debounce = 200 * time.Millisecond
	const pollInterval = 50 * time.Millisecond

	var timer *time.Timer
	fire := make(chan struct{}, 1)

	// lastMtime tracks the mtime we last successfully loaded. Starts at zero so
	// the first poll detects any existing file as a change, covering the race
	// where a write completes before the fsnotify watch is registered.
	var lastMtime time.Time

	schedule := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounce, func() {
			select {
			case fire <- struct{}{}:
			default:
			}
		})
	}

	poll := time.NewTicker(pollInterval)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if filepath.Base(ev.Name) != base ||
				ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			schedule()
		case <-poll.C:
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			mt := info.ModTime()
			if mt.After(lastMtime) {
				// Update lastMtime immediately to prevent the debounce timer from
				// being re-triggered on subsequent polls before the fire is handled.
				lastMtime = mt
				schedule()
			}
		case <-fire:
			// Re-read the current mtime on fire to ensure we compare accurately
			// after the debounce period.
			if info, err := os.Stat(path); err == nil {
				lastMtime = info.ModTime()
			}
			cfg, err := Load(path)
			if err != nil {
				onError(err)
				continue
			}
			onReload(cfg)
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			onError(err)
		}
	}
}
