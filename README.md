# What is this

This is a concurrent directory traversal library. It returns results
via a channel that the caller must service. The caller can specify
what entries are interesting:

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
[go-du](https://github.com/opencoff/go-du) is a simplified `du(1)`
that uses this library. It sorts the output from largest size
utilization to smaller ones.

