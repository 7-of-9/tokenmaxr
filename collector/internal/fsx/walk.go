package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WalkFollow walks each root like filepath.WalkDir, but also descends into
// symbolic links (and, on Windows, junctions) to directories, the roots
// included: a tool's sessions folder moved to another disk and linked back
// is still read. Paths are reported under the names they were reached by.
// Every directory is visited once across all the roots (compared by file
// identity, os.SameFile, which sees through links and junctions alike), so
// a link loop ends and a folder linked twice is not read twice.
//
// fn is called with the error of a root that cannot be read (as WalkDir
// does); fs.SkipDir skips a directory, fs.SkipAll stops the walk.
func WalkFollow(roots []string, fn fs.WalkDirFunc) error {
	w := walker{fn: fn}
	for _, root := range roots {
		st, err := os.Stat(root)
		switch {
		case err != nil:
			err = fn(root, nil, err)
		case !st.IsDir():
			err = fn(root, fs.FileInfoToDirEntry(st), nil)
		case w.visited(st):
			continue
		default:
			// A root may itself be a link into a later root: entered here, it is
			// not entered again as a plain folder there.
			w.links = append(w.links, st)
			err = w.dir(root, st, fs.FileInfoToDirEntry(st))
		}
		if errors.Is(err, fs.SkipAll) {
			return nil
		}
		if err != nil && !errors.Is(err, fs.SkipDir) {
			return err
		}
	}
	return nil
}

type walker struct {
	fn fs.WalkDirFunc
	// dirs are the directories entered so far; links the roots and the
	// targets of the links followed, which a plain directory met later is
	// checked against.
	dirs, links []fs.FileInfo
}

func (w *walker) visited(st fs.FileInfo) bool {
	for _, d := range w.dirs {
		if os.SameFile(d, st) {
			return true
		}
	}
	return false
}

// dir visits path (st is its Stat) and everything under it.
func (w *walker) dir(path string, st fs.FileInfo, d fs.DirEntry) error {
	w.dirs = append(w.dirs, st)
	if err := w.fn(path, d, nil); err != nil {
		return err
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		if err := w.fn(path, d, err); err != nil && !errors.Is(err, fs.SkipDir) {
			return err
		}
		return nil
	}
	for _, e := range ents {
		p := filepath.Join(path, e.Name())
		var err error
		switch {
		case e.IsDir():
			err = w.plain(p, e)
		case e.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0:
			err = w.link(p, e)
		default:
			err = w.fn(p, e, nil)
		}
		if errors.Is(err, fs.SkipDir) {
			if !e.IsDir() {
				// As in WalkDir: SkipDir from a file skips the rest of its directory.
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// plain enters a directory that is not a link, unless a root or a link
// followed earlier already led into it.
func (w *walker) plain(p string, e fs.DirEntry) error {
	// Stat, not e.Info(): its identity is what os.SameFile compares.
	st, err := os.Stat(p)
	if err != nil {
		return w.fn(p, e, err)
	}
	for _, l := range w.links {
		if os.SameFile(l, st) {
			return nil
		}
	}
	return w.dir(p, st, e)
}

// link follows a symbolic link or junction into its directory; a link to a
// file, or a broken one, is reported as the entry it is.
func (w *walker) link(p string, e fs.DirEntry) error {
	st, err := os.Stat(p)
	if err != nil || !st.IsDir() {
		return w.fn(p, e, nil)
	}
	if w.visited(st) {
		return nil // a loop back to a folder above, or one already read
	}
	w.links = append(w.links, st)
	return w.dir(p, st, fs.FileInfoToDirEntry(st))
}
