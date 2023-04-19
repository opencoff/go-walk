// main.go - test driver with real filesystem
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
//

// Ideally, we want to use io/fs and testing/fstest to help here. But,
// io/fs doesn't define Lstat(). And Lstat() is integral to the functionality
// of this library.
//
// So, we create a temp dir and known entries and perform the walk here
//

package main

import (
	"fmt"
	"os"

	"github.com/opencoff/go-walk"
	flag "github.com/opencoff/pflag"
)

func main() {

	var follow, oneFS bool
	var excl []string

	flag.BoolVarP(&follow, "follow-symlinks", "L", false, "follow sym links")
	flag.BoolVarP(&oneFS, "single-fs", "x", false, "don't cross mount points")
	flag.StringArrayVarP(&excl, "exclude", "X", nil, "Exclude glob pattern")

	usage := fmt.Sprintf("%s [options] path...", os.Args[0])

	flag.Usage = func() {
		fmt.Printf("walk-test: Simple test harness for testing the walk\n%s\n", usage)
		flag.PrintDefaults()
	}

	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "%s\n", usage)
		os.Exit(1)
	}

	wo := walk.Options{
		FollowSymlinks: follow,
		OneFS:          oneFS,
		Type:           walk.FILE | walk.SYMLINK | walk.SPECIAL,
		Excludes:       excl,
	}

	errs := walk.WalkFunc(args, &wo, func(nm string, _ os.FileInfo) error {
		fmt.Printf("%s\n", nm)
		return nil
	})

	for i := range errs {
		fmt.Fprintf(os.Stderr, "%s\n", errs[i])
	}
	os.Exit(1 & len(errs))
}
