// walk.go - parallel fs-walker
//
// (c) 2016 Sudhi Herle <sudhi@herle.net>
//
// Licensing Terms: GPLv2
//
// If you need a commercial license for this work, please contact
// the author.
//
// This software does not come with any express or implied
// warranty; it is provided "as is". No claim  is made to its
// suitability for any purpose.

package walk

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

const (
	// the channels used for internal use and callers are all buffered.
	// We don't want the producers to be blocked.
	_Chansize int = 4096

	// we use one worker per CPU core for the concurrent walker.
	// ParallelismFactor multiples the number of go-routines.
	_ParallelismFactor int = 2
)

// Options control the behavior of the filesystem walk
type Options struct {
	// Follow symlinks if set
	FollowSymlinks bool

	// stay within the same file-system
	OneFS bool

	// Exclude names starting with this list
	Excludes []string
}

// Type denotes the walk type - it is a way to filter the results returned by
// Walk()
type Type uint

const (
	_none Type = 1 << iota

	// walk only regular files
	FILE

	// walk only directories
	DIR

	// walk only special files (symlinks, sockets, devices etc)
	SPECIAL

	// short hand for all of above
	ALL = FILE | DIR | SPECIAL
)

// Result is the data returned as part of the directory walk
type Result struct {
	// path relative to the supplied argument
	Path string

	// stat(2) info
	Stat os.FileInfo
}

// internal state
type walkState struct {
	Options
	typ   Type
	ch    chan string
	out   chan Result
	errch chan error

	// Tracks completion of the DFS walk across directories.
	// Each counter in this waitGroup tracks one subdir
	// we've encountered.
	wg sync.WaitGroup

	singlefs func(fi os.FileInfo, nm string) bool

	// Tracks device major:minor to detect mount-point crossings
	fs sync.Map
}

// Walk traverses the entries in 'names' in a concurrent fashion and returns
// results in a channel of Result. The caller must service the channel. Any errors
// encountered during the walk are returned in the error channel.
func Walk(names []string, typ Type, opt *Options) (chan Result, chan error) {

	if opt == nil {
		opt = &Options{}
	}

	d := &walkState{
		Options: *opt,
		typ:     typ,
		ch:      make(chan string, _Chansize),
		out:     make(chan Result, _Chansize),
		errch:   make(chan error, 8),
		singlefs: func(os.FileInfo, string) bool {
			return true
		},
	}

	if opt.OneFS {
		d.singlefs = d.isSingleFS
	}

	// start workers; they will end when the channel is closed; we don't need
	// any waitgroups to track these goroutines.
	nworkers := runtime.NumCPU() * _ParallelismFactor
	for i := 0; i < nworkers; i++ {
		go d.worker()
	}

	// send work to workers
	dirs := make([]string, 0, len(names))
	for i := range names {
		var fi os.FileInfo
		var err error

		nm := strings.TrimSuffix(names[i], "/")
		if len(nm) == 0 {
			nm = "/"
		}

		if d.exclude(nm) {
			continue
		}

		fi, err = os.Lstat(nm)
		if err != nil {
			d.errch <- fmt.Errorf("lstat %s: %w", nm, err)
			continue
		}

		m := fi.Mode()
		switch {
		case m.IsDir():
			// we only give dirs to workers
			if opt.OneFS {
				d.trackFS(fi, nm)
			}
			dirs = append(dirs, nm)

		case m.IsRegular():
			if (d.typ & FILE) > 0 {
				d.out <- Result{nm, fi}
			}

		case (m & os.ModeSymlink) > 0:
			// we may have new info now. The symlink may point to file, dir or
			// special.
			if d.doSymlink(nm, fi) {
				dirs = append(dirs, nm)
			}

		default:
			if (d.typ & SPECIAL) > 0 {
				d.out <- Result{nm, fi}
			}
		}
	}

	// queue the dirs
	if len(dirs) > 0 {
		d.enq(dirs)
	}

	// close the channels when we're all done
	go func() {
		d.wg.Wait()
		close(d.out)
		close(d.errch)
		close(d.ch)
	}()

	return d.out, d.errch
}

// worker thread to walk directories
func (d *walkState) worker() {
	for nm := range d.ch {

		fi, err := os.Lstat(nm)
		if err != nil {
			d.errch <- fmt.Errorf("lstat %s: %w", nm, err)
			d.wg.Done()
			continue
		}

		// we are _sure_ this is a dir.

		if (d.typ & DIR) > 0 {
			d.out <- Result{nm, fi}
		}

		dirs, err := d.walkPath(nm)
		if err != nil {
			d.errch <- err
			d.wg.Done()
			continue
		}

		if len(dirs) > 0 {
			d.enq(dirs)
		}

		d.wg.Done()
	}
}

// return true if nm needs to be excluded
func (d *walkState) exclude(nm string) bool {
	for _, v := range d.Excludes {
		if strings.HasPrefix(nm, v) {
			return true
		}
	}

	return false
}

// enqueue a list of dirs in a separate go-routine so the caller is
// not blocked (deadlocked)
func (d *walkState) enq(dirs []string) {
	d.wg.Add(len(dirs))
	go func() {
		for i := range dirs {
			d.ch <- dirs[i]
		}
	}()
}

// process a directory and return the list of subdirs and a total of all regular
// file sizes
func (d *walkState) walkPath(nm string) (dirs []string, err error) {
	fd, err := os.Open(nm)
	if err != nil {
		return nil, err
	}
	defer fd.Close()

	fiv, err := fd.Readdir(-1)
	if err != nil {
		return nil, err
	}

	// hack to make joined paths not look like '//file'
	if nm == "/" {
		nm = ""
	}

	dirs = make([]string, 0, len(fiv)/2)
	for i := range fiv {
		fi := fiv[i]
		m := fi.Mode()

		// we don't want to use filepath.Join() because it "cleans"
		// the path (removes the leading .)
		fp := fmt.Sprintf("%s/%s", nm, fi.Name())

		switch {
		case m.IsDir():
			// we only have to worry about mount points
			if !d.singlefs(fi, fp) {
				continue
			}
			dirs = append(dirs, fp)

		case m.IsRegular():
			if (d.typ & FILE) > 0 {
				d.out <- Result{fp, fi}
			}

		case (m & os.ModeSymlink) > 0:
			// we may have new info now. The symlink may point to file, dir or
			// special.
			if d.doSymlink(fp, fi) {
				dirs = append(dirs, fp)
			}

		default:
			if (d.typ & SPECIAL) > 0 {
				d.out <- Result{fp, fi}
			}
		}
	}

	return dirs, nil
}

// Walk symlinks - we let the kernel follow the symlinks and resolve any loops.
// This function returns true if 'nm' ends up being a directory that we must descend.
func (d *walkState) doSymlink(nm string, fi os.FileInfo) bool {

	if !d.FollowSymlinks {
		d.out <- Result{nm, fi}
		return false
	}

	fi, err := os.Stat(nm)
	if err != nil {
		d.errch <- err
		return false
	}

	m := fi.Mode()
	switch {
	case m.IsDir():
		// we only have to worry about mount points
		if d.singlefs(fi, nm) {
			return true
		}

	case m.IsRegular():
		if (d.typ & FILE) > 0 {
			d.out <- Result{nm, fi}
		}

	case (m & os.ModeSymlink) > 0:
		// This should never happen since the kernel was walked the symlink chain
		panic(fmt.Sprintf("walk: symlink %s yielded another symlink!", nm))

	default:
		if (d.typ & SPECIAL) > 0 {
			d.out <- Result{nm, fi}
		}
	}

	return false
}

// track this file for future mount points
func (d *walkState) trackFS(fi os.FileInfo, nm string) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		d.fs.Store(st.Dev, nm)
	}
}

// Return true if the inode is on the same file system as the command line args
func (d *walkState) isSingleFS(fi os.FileInfo, nm string) bool {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if _, ok := d.fs.Load(st.Dev); ok {
			return true
		}
	}

	return false
}

// EOF
