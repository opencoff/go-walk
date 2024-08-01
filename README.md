[![GoDoc](https://godoc.org/github.com/opencoff/go-walk?status.svg)](https://godoc.org/github.com/opencoff/go-walk)
# What is this

This is a concurrent directory traversal library. It returns each entry via
a channel or via a caller supplied function (ie callback). In either case,
the caller can specify what entries are interesting:

* Files
* Directories
* Special files (symlinks, device nodes etc.)
* All of the above

It can optionally follow symlinks and detect mount-point crossings.

# How can I use it?
Here is an example program:
```go

    dirs := []string{"/etc", "/usr", "/bin", "/sbin", "/lib"}
    opt := walk.Options{
            OneFS: true,
            FollowSymlinks: true,
    }

    ch, errch := walk.Walk(dirs, walk.FILE, &opt)

    go func() {
        for err := range errch {
            fmt.Printf("walk: %s\n", err)
        }
    }()

    // harvest results
    for r := range ch {
        fmt.Printf("%s: %d bytes\n", r.Path, r.Stat.Size())
    }

```

# Who's using this?
[go-progs](https://github.com/opencoff/go-progs) is a collection of go tools
including a simpler implementation of du(1).

## Licensing Terms
The tool and code is licensed under the terms of the
GNU Public License v2.0 (strictly v2.0). If you need a commercial
license or a different license, please get in touch with me.

See the file ``LICENSE`` for the full terms of the license.
