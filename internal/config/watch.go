package config

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ResolvePath returns the config file Load(flagConfig) would read, and ok=false
// for an env-only run (no file to watch). When flagConfig is set, ok reports
// whether that file exists.
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
	var timer *time.Timer
	fire := make(chan struct{}, 1)
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
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounce, func() {
				select {
				case fire <- struct{}{}:
				default:
				}
			})
		case <-fire:
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
