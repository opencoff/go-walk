// walk_test.go -- test harness for walk.go

package walk

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func newAsserter(t *testing.T) func(cond bool, msg string, args ...interface{}) {
	return func(cond bool, msg string, args ...interface{}) {
		if cond {
			return
		}

		_, file, line, ok := runtime.Caller(1)
		if !ok {
			file = "???"
			line = 0
		}

		s := fmt.Sprintf(msg, args...)
		t.Fatalf("%s: %d: Assertion failed: %s\n", file, line, s)
	}
}

type test struct {
	dir string
	typ Type
}

var tests = []test{
	{"$HOME/.config", FILE | SYMLINK},
}

var linuxTests = []test{
	{"/dev", ALL},
	{"/dev", DEVICE},
	{"/dev", SPECIAL},
	{"/dev", DIR | SYMLINK},
}

var macOSTests = []test{
	{"/etc", ALL},
	{"$HOME/Library", ALL},
	{"$HOME/Library", FILE | SYMLINK},
}

func newWalk(tx *test) (map[string]fs.FileInfo, []error) {
	nm := os.ExpandEnv(tx.dir)
	names := [...]string{nm}
	opt := &Options{
		FollowSymlinks: false,
		OneFS:          false,
		Type:           tx.typ,
	}

	res := make(map[string]fs.FileInfo)
	och, ech := Walk(names[:], opt)

	var wg sync.WaitGroup

	wg.Add(1)
	var errs []error
	go func() {
		for e := range ech {
			errs = append(errs, e)
		}
		wg.Done()
	}()

	for o := range och {
		res[o.Path] = o.Stat
	}

	wg.Wait()
	return res, errs
}

func oldWalk(tx *test) (map[string]fs.FileInfo, []error) {
	var m os.FileMode

	ty := tx.typ
	for k, v := range typMap {
		if (k & ty) > 0 {
			m |= v
		}
	}

	predicate := func(mode fs.FileMode) bool {
		if (m&mode) > 0 || ((ty&FILE) > 0 && mode.IsRegular()) {
			return true
		}
		return false
	}

	res := make(map[string]fs.FileInfo)
	var errs []error

	nm := os.ExpandEnv(tx.dir)
	err := filepath.WalkDir(nm, func(p string, di fs.DirEntry, e error) error {
		if e != nil {
			errs = append(errs, e)
			return nil
		}

		if !predicate(di.Type()) {
			return nil
		}

		// we're interested in this entry
		fi, err := di.Info()
		if err != nil {
			errs = append(errs, err)
		} else {
			res[p] = fi
		}
		return nil
	})

	if err != nil {
		errs = append(errs, err)
	}

	return res, errs
}

func toString(v []error) string {
	var x []string

	for _, e := range v {
		x = append(x, fmt.Sprintf("%s", e))
	}
	return strings.Join(x, "\n")
}

func TestWalk(t *testing.T) {

	switch runtime.GOOS {
	case "linux":
		tests = append(tests, linuxTests...)

	case "darwin":
		tests = append(tests, macOSTests...)
	}

	for i := range tests {
		tx := &tests[i]

		t.Run(tx.dir, func(t *testing.T) {
			t.Parallel()
			assert := newAsserter(t)

			var wg sync.WaitGroup
			var r1, r2 map[string]fs.FileInfo
			var e1, e2 []error

			wg.Add(2)
			go func(tx *test) {
				r2, e2 = newWalk(tx)
				wg.Done()
			}(tx)

			go func(tx *test) {
				r1, e1 = oldWalk(tx)
				wg.Done()
			}(tx)

			wg.Wait()
			assert(len(e2) == 0, "%d: Errors new-walk %s:\n%s\n",
				i, tx.dir, toString(e2))
			assert(len(e1) == 0, "%d: Errors old-walk %s:\n%s\n",
				i, tx.dir, toString(e1))

			for k, _ := range r1 {
				_, ok := r2[k]
				assert(ok, "%d %s: can't find %s in new walk", i, tx.dir, k)
				delete(r2, k)
			}

			// now we know that everything the stdlib.Walk found is also present
			// in our concurrent-walker.

			if len(r2) > 0 {
				var rem []string

				for k := range r2 {
					rem = append(rem, k)
				}
				t.Fatalf("new walk has extra entries:\n%s\n",
					strings.Join(rem, "\n"))
			}
		})
	}
}
