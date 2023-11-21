// walk.go - parallel fs-walker
//
// (c) 2022- Sudhi Herle <sudhi@herle.net>
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
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

const (

	// we use one worker per CPU core for the concurrent walker.
	// ParallelismFactor multiples the number of go-routines.
	_ParallelismFactor int = 2

	// Max number of consecutive symlinks we will follow
	_MaxSymlinks int = 100
)

// the channels used for internal use and callers are all buffered.
// We don't want the producers to be blocked.
var _Chansize = _ParallelismFactor * runtime.NumCPU()

type Type uint

const (
	FILE Type = 1 << iota
	DIR
	SYMLINK
	DEVICE
	SPECIAL

	// This is a short cut for "give me all entries"
	ALL = FILE | DIR | SYMLINK | DEVICE | SPECIAL
)

// Options control the behavior of the filesystem walk
type Options struct {
	// Follow symlinks if set
	FollowSymlinks bool

	// stay within the same file-system
	OneFS bool

	// if set, return xattr for every returned result
	Xattr bool

	// Types of entries to return
	Type Type

	// Excludes is a list of shell-glob patterns to exclude from
	// the walk. If a dir matches the prefix, go-walk does
	// not descend that subdirectory.
	Excludes []string

	// Filter is an optional caller provided callback
	// This function must return True if this entry should
	// no longer be processed. ie filtered out.
	Filter func(nm string, fi os.FileInfo) bool
}

// Result is the data returned as part of the directory walk
type Result struct {
	// path relative to the supplied argument
	Path string

	// stat(2) info
	Stat os.FileInfo

	// extended attributes for this file
	// set only if user requests it
	Xattr Xattr
}

// internal state
type walkState struct {
	Options
	ch    chan string
	out   chan Result
	errch chan error

	// type mask for output filtering
	typ os.FileMode

	// Tracks completion of the DFS walk across directories.
	// Each counter in this waitGroup tracks one subdir
	// we've encountered.
	dirWg sync.WaitGroup

	// Tracks worker goroutines
	wg sync.WaitGroup

	singlefs func(nm string, fi os.FileInfo) bool

	// the output action - either send info via chan or call user supplied func
	apply func(nm string, fi os.FileInfo)

	// Tracks device major:minor to detect mount-point crossings
	fs  sync.Map
	ino sync.Map
}

// mapping our types to the stdlib types
var typMap = map[Type]os.FileMode{
	FILE:    0,
	DIR:     os.ModeDir,
	SYMLINK: os.ModeSymlink,
	DEVICE:  os.ModeDevice | os.ModeCharDevice,
	SPECIAL: os.ModeNamedPipe | os.ModeSocket,
}

var strMap = map[Type]string{
	FILE:    "File",
	DIR:     "Dir",
	SYMLINK: "Symlink",
	DEVICE:  "Device",
	SPECIAL: "Special",
}

// Stringer for walk filter Type
func (t Type) String() string {
	var z []string
	for k, v := range strMap {
		if (k & t) > 0 {
			z = append(z, v)
		}
	}
	return strings.Join(z, "|")
}

// Walk traverses the entries in 'names' in a concurrent fashion and returns
// results in a channel of Result. The caller must service the channel. Any errors
// encountered during the walk are returned in the error channel.
func Walk(names []string, opt *Options) (chan Result, chan error) {
	out := make(chan Result, _Chansize*2)
	d := newWalkState(opt)

	// This function sends output to a chan
	d.apply = func(nm string, fi os.FileInfo) {
		r := Result{
			Path: nm,
			Stat: fi,
		}
		if d.Xattr {
			x, err := getxattr(nm)
			if err != nil {
				d.errch <- err
				return
			}
			r.Xattr = x
		}
		out <- r
	}

	d.doWalk(names)

	// close the channels when we're all done
	go func() {
		d.dirWg.Wait()
		close(d.ch)
		close(out)
		close(d.errch)
		d.wg.Wait()
	}()

	return out, d.errch
}

// WalkFunc traverses the entries in 'names' in a concurrent fashion and calls 'apply'
// for entries that match criteria in 'opt'. The apply function must be concurrency-safe
// ie it will be called concurrently from multiple go-routines. Any errors reported by
// 'apply' will be returned from WalkFunc().
func WalkFunc(names []string, opt *Options, apply func(r Result) error) []error {
	d := newWalkState(opt)

	// This calls the caller supplied 'apply' func
	d.apply = func(nm string, fi os.FileInfo) {
		r := Result{
			Path: nm,
			Stat: fi,
		}

		if d.Xattr {
			x, err := getxattr(nm)
			if err != nil {
				d.errch <- err
				return
			}
			r.Xattr = x
		}

		if err := apply(r); err != nil {
			d.errch <- err
			return
		}
	}

	d.doWalk(names)

	// harvest errors and prepare to return
	var errWg sync.WaitGroup
	var errs []error

	errWg.Add(1)
	go func(in chan error) {
		for e := range in {
			errs = append(errs, e)
		}
		errWg.Done()
	}(d.errch)

	// close the channels when we're all done
	d.dirWg.Wait()
	close(d.ch)
	close(d.errch)
	errWg.Wait()
	d.wg.Wait()

	return errs
}

func newWalkState(opt *Options) *walkState {
	if opt == nil {
		opt = &Options{}
	}

	d := &walkState{
		Options: *opt,
		ch:      make(chan string, _Chansize),
		errch:   make(chan error, 8),
		singlefs: func(string, os.FileInfo) bool {
			return true
		},
	}
	return d
}

func (d *walkState) doWalk(names []string) {
	if d.OneFS {
		d.singlefs = d.isSingleFS
	}

	// default accept filter
	if d.Filter == nil {
		// by default - "don't filter anything"
		d.Filter = func(string, os.FileInfo) bool {
			return false
		}
	}

	// build a fast lookup of our types to stdlib
	t := d.Type
	for k, v := range typMap {
		if (t & k) > 0 {
			d.typ |= v
		}
	}

	nworkers := runtime.NumCPU() * _ParallelismFactor
	d.wg.Add(nworkers)
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
			d.error("lstat %s: %w", nm, err)
			continue
		}

		// don't process entries we've already seen
		if d.isEntrySeen(nm, fi) {
			continue
		}

		if d.Filter(nm, fi) {
			continue
		}

		m := fi.Mode()
		switch {
		case m.IsDir():
			if d.OneFS {
				d.trackFS(fi, nm)
			}
			dirs = append(dirs, nm)

		case (m & os.ModeSymlink) > 0:
			// we may have new info now. The symlink may point to file, dir or
			// special.
			dirs = d.doSymlink(nm, fi, dirs)

		default:
			d.output(nm, fi)
		}
	}

	// queue the dirs
	d.enq(dirs)

}

// worker thread to walk directories
func (d *walkState) worker() {
	for nm := range d.ch {
		fi, err := os.Lstat(nm)
		if err != nil {
			d.error("lstat %s: %w", nm, err)
			d.dirWg.Done()
			continue
		}

		// we are _sure_ this is a dir.
		d.output(nm, fi)

		// Now process the contents of this dir
		d.walkPath(nm)

		// It is crucial that we do this as the last thing in the processing loop.
		// Otherwise, we have a race condition where the workers will prematurely quit.
		// We can only decrement this wait-group _after_ walkPath() has returned!
		d.dirWg.Done()
	}

	d.wg.Done()
}

// output action for entries we encounter
func (d *walkState) output(nm string, fi os.FileInfo) {
	m := fi.Mode()

	// we have to special case regular files because there is
	// no mask for Regular Files!
	//
	// For everyone else, we can consult the typ map
	if (d.typ&m) > 0 || ((d.Type&FILE) > 0 && m.IsRegular()) {
		d.apply(nm, fi)
	}
}

// return true iff basename(nm) matches one of the patterns
func (d *walkState) exclude(nm string) bool {
	if len(d.Excludes) == 0 {
		return false
	}

	bn := path.Base(nm)
	for _, pat := range d.Excludes {
		ok, err := path.Match(pat, bn)
		if err != nil {
			d.errch <- fmt.Errorf("glob '%s': %s", pat, err)
		} else if ok {
			return true
		}
	}

	return false
}

// enqueue a list of dirs in a separate go-routine so the caller is
// not blocked (deadlocked)
func (d *walkState) enq(dirs []string) {
	if len(dirs) > 0 {
		d.dirWg.Add(len(dirs))
		go func(dirs []string) {
			for _, nm := range dirs {
				d.ch <- nm
			}
		}(dirs)
	}
}

// Process a directory and return the list of subdirs
//
// There is *no* race condition between the workers reading d.ch and the
// wait-group going to zero: there is at least 1 count outstanding: of the
// current entry being processed. So, this function can take as long as it wants
// the caller (d.worker()) won't decrement that wait-count until this function
// returns. And by then the wait-count would've been bumped up by the number of
// dirs we've seen here.
func (d *walkState) walkPath(nm string) {
	fd, err := os.Open(nm)
	if err != nil {
		d.error("%s: %s", nm, err)
		return
	}
	defer fd.Close()

	fiv, err := fd.Readdir(-1)
	if err != nil {
		d.error("%s: %s", nm, err)
		return
	}

	// hack to make joined paths not look like '//file'
	if nm == "/" {
		nm = ""
	}

	dirs := make([]string, 0, len(fiv)/2)
	for i := range fiv {
		fi := fiv[i]
		m := fi.Mode()

		// we don't want to use filepath.Join() because it "cleans"
		// the path (removes the leading .)
		fp := fmt.Sprintf("%s/%s", nm, fi.Name())

		if d.exclude(fp) {
			continue
		}

		// don't process entries we've already seen
		if d.isEntrySeen(nm, fi) {
			continue
		}

		if d.Filter(fp, fi) {
			continue
		}

		switch {
		case m.IsDir():
			// don't descend if this directory is not on the same file system.
			if d.singlefs(fp, fi) {
				dirs = append(dirs, fp)
			}

		case (m & os.ModeSymlink) > 0:
			// we may have new info now. The symlink may point to file, dir or
			// special.
			dirs = d.doSymlink(fp, fi, dirs)

		default:
			d.output(fp, fi)
		}
	}

	d.enq(dirs)
}

// Walk symlinks and don't process dirs/entries that we've already seen
// This function returns true if 'nm' ends up being a directory that we must descend.
func (d *walkState) doSymlink(nm string, fi os.FileInfo, dirs []string) []string {
	if !d.FollowSymlinks {
		d.output(nm, fi)
		return dirs
	}

	// process symlinks until we are done
	newnm, err := filepath.EvalSymlinks(nm)
	if err != nil {
		d.error("symlink %s: %s", nm, err)
		return dirs
	}
	nm = newnm

	// we know this is no longer a symlink
	fi, err = os.Stat(nm)
	if err != nil {
		d.error("stat %s: %s", nm, err)
		return dirs
	}

	// do rest of processing iff we haven't seen this entry before.
	if !d.isEntrySeen(nm, fi) {
		switch {
		case fi.Mode().IsDir():
			// we only have to worry about mount points
			if d.singlefs(nm, fi) {
				dirs = append(dirs, nm)
			}
		default:
			d.output(nm, fi)
		}
	}

	return dirs
}

// track this inode to detect loops; return true if we've seen it before
// false otherwise.
func (d *walkState) isEntrySeen(nm string, fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}

	key := fmt.Sprintf("%d:%d:%d", st.Dev, st.Rdev, st.Ino)
	x, ok := d.ino.LoadOrStore(key, st)
	if !ok {
		return false
	}

	// This can't fail because we checked it above before storing in the
	// sync.Map
	xt := x.(*syscall.Stat_t)

	//fmt.Printf("# %s: old ino: %d:%d:%d  <-> new ino: %d:%d:%d\n", nm, xt.Dev, xt.Rdev, xt.Ino, st.Dev, st.Rdev, st.Ino)

	if xt.Dev != st.Dev || xt.Rdev != st.Rdev || xt.Ino != st.Ino {
		return false
	}

	// We have to check one more time to see if the resolved symlink
	// crosses mountpoints.
	return d.isSingleFS(nm, fi)
}

// track this file for future mount points
// We call this function once for each entry passed to Walk().
func (d *walkState) trackFS(fi os.FileInfo, nm string) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		key := fmt.Sprintf("%d:%d", st.Dev, st.Rdev)
		d.fs.Store(key, nm)
	}
}

// Return true if the inode is on the same file system as the command line args
func (d *walkState) isSingleFS(nm string, fi os.FileInfo) bool {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		key := fmt.Sprintf("%d:%d", st.Dev, st.Rdev)
		if _, ok := d.fs.Load(key); ok {
			return true
		}
	}

	return false
}

// enq an error
func (d *walkState) error(s string, args ...any) {
	d.errch <- fmt.Errorf(s, args...)
}

// EOF
